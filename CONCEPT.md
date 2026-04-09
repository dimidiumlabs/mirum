# The Concept Document

> [!CAUTION]
> This document describes how I would like to design the system;
> it doesn't reflect the current state. Almost everything is still unimplemented.

The really portable CI platform with VM-first isolation,
programmable pipelines, and local execution parity.

## [Why](https://xkcd.com/927/)

The goal of this project is to build a CI system that's both convenient for tiny
projects and suitable for gigantic C++ codebases like Chromium or llvm.

For this to work, several key decisions need to be made:

**Open source.**
You can and should build SaaS, but the user must be able to deploy the entire
system themselves. Self-hosted runners aren't enough.

**Portability and cross-platform support.**
You should be able to run on at least x64, arm64, riscv64, powerpc, s390x, and
loongarch64, as well as Linux, {Free,Open,Net}BSD, Windows, and macOS. Users
should be able to add exotic features like Haiku and Plan9, or even a custom kernel.

**Hermitic builds.**
All official platforms should support sealed builds with declarative environment
descriptions (like Docker, yes). Hosted builds should work everywhere.

**Different deployment models.**
Not everyone can deploy themselves, and not everyone wants to. Offer open-source
SaaS, cloud and self-hosted runners, and fully autonomous solutions. The degree
of autonomy is the user's choice.

**Dynamic pipelines.**
Don't assume that all tasks are described by a static YAML/TOML configuration.
This is often the case, but you should have a path for dynamic pipelines when they
are needed.

**Security and Isolation.**
CI is the most security-sensitive platform, affecting testing, deployment to production,
and releases.
CI has access to both the code and the production environment.
Since the "just hide it behind a VPN" option doesn't work for either SaaS or open
source projects where CI must be public, you can't be overly paranoid about architectural
decisions. Consider that the code for your pacemaker might be tested here.

**Local debugging.**
You should be able to run the build locally, get the console into the sandbox, and
attach a debugger. There's nothing more pointless and merciless than trying to fix
automation than the cycle of "test commit -> push to Git Forge -> hope it works."

- `mirum run` — run the pipeline locally with the same VMs but with a local copy
  of your code and a debugger
- `mirum ssh` — connect to a failed VM over SSH and debug on real hardware
- `mirum try` — run a build from local changes on the cluster without creating
  throwaway commits.

## How

The architecture is built on two key solutions:
[Virtual Machines](https://en.wikipedia.org/wiki/Virtualization#Hardware_virtualization)
and [Starlark](https://starlark-lang.org/).

### Virtualization vs. Containers vs. Host

Modern CI must provide a reproducible and hermetic environment for every task.
Despite the popularity of container isolation, it's a Linux-specific technology
with many limitations. As soon as you need Windows, a specific kernel version,
or even systemd, you're forced to revert to bare, stateful runners you've manually
configured.

A solution was proposed in Sourcehut: use ephemeral VMs from a snapshot for isolated
builds. Unlike a container, a VM can run any guest system, can emulate inaccessible
architectures, provides a full stack including the kernel, and provides sufficient
isolation to allow a user to access the build machine via SSH.

The idea is to split the runner into two layers:

- mirum-agent, a highly portable statically linked C binary that can copy files,
  execute bash commands, and collect logs and resources.
- mirum-worker, a full-fledged runtime that launches a disposable VM for each task,
  launching and managing mirum-agent within the VM.

This architecture allows for full support on official platforms, including isolation,
SSH access, and so on. On the other hand, if you're testing an exotic system
(for example, on bare metal without any OS at all), port mirum-agent and you'll
be able to connect to a regular mirum server. You can also offer specialized versions
of mirum-worker for containers/clouds/lambdas, or any custom environment.
Simply run mirum-agent in a sandbox and issue commands to it.

Using VMs also significantly simplifies infrastructure.
One x64 host can run Linux, Windows, and *BSD,
while one arm64 mac mini can run macOS, Linux, and Windows on arm64.

### Starlark

Today, there are two ways to describe pipelines: statically in yaml or by writing
a script in a scripting language like JavaScript/Python/Ruby.

Mirum occupies a niche between the two and offers Starlark as a configuration language.
Starlark is a specialized embedded programming language developed for the Bazel
build system. On the one hand, you have variable conditions and loops, imports,
objects, and arrays.

Unlike yaml, you don't have to reinvent the wheel. Settings are variables, tasks
are functions, build matrices are loops, and the reusable actions library is
a simple import. Python syntax is well-known and doesn't require learning your DSL.

On the other hand, it's not an algorithmically complete language. There are no side
effects, no need for a separate sandbox, and no need to drag in a runtime. The CI
server has total control over Starlark execution. But unlike Kotlin DSL (TeamCity),
Groovy (Jenkins), or Python (Buildbot), Starlark forbids side effects: no network,
no filesystem, no arbitrary imports. Eval is safe for untrusted code
(PRs from external contributors), deterministic, and cacheable.

## Modules

Four executable modules:

```
┌──────────────────────────────────────────────────────┐
│                      mirum-server                    │
│  watch registry, task queue, log aggregation, WebUI  │
└─────────────────────────────────────────────────────┘
         ↑ gRPC (worker-initiated)          ↑  gRPC
         │                                  │
┌────────┴──────────┐             ┌─────────┴─────────┐
│   mirum-worker    │             │   mirum-worker    │
│  Linux (KVM)      │             │  macOS (Vz)       │
│     │ vsock       │             │     │ vsock       │
│     ↓             │             │     ↓             │
│  ┌────────────┐   │             │  ┌────────────┐   │
│  │mirum-agent │   │             │  │mirum-agent │   │
│  │ inside VM  │   │             │  │ inside VM  │   │
│  └────────────┘   │             │  └────────────┘   │
└───────────────────┘             └───────────────────┘

┌───────────────────┐
│   mirum (CLI)     │
│  Starlark eval,   │
│  spawns worker    │
└───────────────────┘
```

**mirum-server** — the orchestrator.
Contains a database, stores users, organizations, project and pipeline settings,
provides a WebUI and API, responds to webhooks, and distributes tasks to workers.
Collects logs and build results from workers.

It's a control plane.

**mirum-worker** —  the task executor
Connects to the server via outbound gRPC, declares its capabilities,
and picks tasks from the queue that it can execute.

Executes starlark (project + pipeline functions, coroutine model) and notifies
the server of changes (for example, new watch settings).
Starts a VM with tasks and returns the results to the server. If starlark is blocked
on yeald, it leaves the blocked coroutine in the queue.

It's a data plane. _Only the worker has access to the user's secrets and code._

Workers come in different kinds: KVM worker for local VMs,
macOS worker with Vz.framework, windows worker with Hyper-V,
EC2 worker for cloud VMs, host worker for direct execution.
A new worker type joins the cluster and starts picking up tasks with no changes
to the server.

**mirum-agent** — static binary pre-installed in every VM.

A tiny bridge between the worker on the host and the tasks running in the VM.
Written in C99, no dependencies, posix-only. Implements a simple TLV for communicating
with the host (via virtio-vsock or tcp socket, depending on the hypervisor).

Channels: control, stdin, stdout, stderr, file transfers, interactive shell sessions.

Small and simple enough to be auditable.
This component lives close to the actual code, making it highly security-sensitive.

**mirum (CLI)** — developer tool.
Contains a Starlark runtime for local eval — takes the server's role locally.

Technically, it is a lightweight disposable server that runs a real worker locally
to replicate real-world conditions in a cluster as closely as possible.

`mirum run`: eval pipeline → spawn worker → dispatch tasks → display logs.
`mirum try`: send a diff to the server.
`mirum attach`: shell into a VM.
`mirum eval`: show the DAG without running anything.

## Configuration Model

Mirum operates on projects, a project consists of pipelines,
pipelines consist of tasks. A task is the minimum unit of execution — it always
runs in a single VM. Pipelines are DAGs (directed acyclic graphs) that invoke multiple
tasks and pass state between them. A project tracks a list of pipelines, their trigger
rules (watch, cron, manual, ...), and result notifications.

A quick example pipeline, all in one starlark file:

```python
# /.mirum/main.star — entry point and the only file required

# A single pipeline describes a matrix of multiple operating systems and architectures.
# A single Linux host with KVM serves Linux, Windows, and BSD guests.
# A macOS host serves macOS, Linux, and BSD.
# Tasks adapt to the platform via `ctx.os` — they don't choose it.

# ctx.run takes an optional setup function to indicate which steps
# are environment setup only, so the resulting image can be cached
def setup(ctx):
    ctx.shell("apt-get update && apt-get install -y cargo")

def build(ctx):
    ctx.checkout()
    ctx.shell("cargo build --release")
    ctx.upload("target/release/myapp", artifact="bin")

def test(ctx):
    # test knows nothing about build — only that it needs an artifact.
    # that artifact could have been built right here, come from cache
    # or a registry, or even uploaded from a developer's laptop
    ctx.download("bin", dest=".")
    ctx.shell("cargo test")

# Write a function that describes your pipeline.
def build_pipeline(ctx):
    src = ctx.source()

    # Build steps are simply functions that the pipeline calls in the VM sandbox.
    b = ctx.run(build, setup=setup, source=src, image="mirum/ubuntu-24.04")
    ctx.run(test, setup=setup, depends=b, image="mirum/ubuntu-24.04")

# Describe your project, what pipelines exist, and how to launch them.
def project(ctx):
    ctx.pipeline("build", fn=build_pipeline, watch=ctx.watch(events=["push"], manual=True))
```

### Functions All the Way Down

Mirum configuration is nested function composition in Starlark.
`project` is the entry point and the only required function — it registers
pipelines and the rules for "what, from where, and when to run."

A pipeline is a function that imperatively executes tasks one by one and passes
dependencies between them. A task's result can be used to launch further tasks,
enabling dynamic task creation. You don't need separate syntax for
"allow failure / retry / skip_if / matrix" — it's just if/for in Starlark.

```
# /.mirum/main.star — the entry point the server looks for

project(ctx)  → event routing       "when to trigger"

pipeline(ctx) → DAG of tasks        "what to run, on which platforms"

task(ctx)     → scripts + artifacts  "how to build"
```

Starlark supports imports, so splitting a large project works out of the box.
The only requirement is a `.mirum/` directory at the repository root with `main.star`
as the entry point. The internal structure of `.mirum/` is free-form.

The fixed `.mirum/` path is not a convention but an architectural requirement.
The server reads configuration via the forge contents API (`GET /repos/Org/repo/contents/.mirum/`),
not via git clone. This allows: fetching only configuration files without accessing
source code, filtering webhooks (push with no changes in `.mirum/` → skip re-eval),
caching configuration by the directory's commit SHA.

To reuse code both within and outside the project, starlark load is used.

```
@mirum//   Standard library (services, apt, bazel helpers)
@pkg//     External packages (from deps.star, pinned by commit)
//         Local files (relative to .mirum/ root)
```

### Coroutine Eval: Dynamic DAGs

Pipeline functions execute as coroutines. `ctx.run()` without accessing results
is non-blocking — the server accumulates pending tasks. Accessing a result (`.output()`)
is a yield point: the server dispatches all pending tasks, waits for the needed
result, and resumes the pipeline function.

```python
def build(ctx):
    ...

def discover_tests(ctx):
    ...

def run_test(ctx):
    ...

def pipeline(ctx):
    src = ctx.source()  # where sources come from: git, mirum try, or a local directory

    # All ctx.run() calls before the first .output() accumulate and run in parallel
    builds = {}
    for os in ["linux", "mac"]:
        builds[os] = ctx.run(build, source=src,
            image="mirum/%s" % os, args={"os": os})

    discovery = ctx.run(discover_tests, source=src,
        image="mirum/linux")

    # Yield: server dispatches builds + discovery in parallel,
    # waits for discovery to complete, resumes with result
    test_modules = discovery.output("modules")

    # Dynamic phase: use runtime data
    for module in test_modules:
        ctx.run(run_test, args={"module": module},
            depends=list(builds.values()))
```

If a pipeline function never calls `.output()`, the entire DAG is built in
a single pass. A static DAG is a special case of the dynamic model.

`mirum eval` executes the pipeline function locally without dispatching tasks.
For static DAGs it prints the full graph. For dynamic DAGs — everything up to
the first yield point, marked "depends on runtime data beyond this point."

### Example: Everything in One File

```python
# /.mirum/main.star — complete CI

# ctx.run takes an optional setup function to indicate which steps
# are environment setup only, so the resulting image can be cached
def setup(ctx):
    ctx.shell("apt-get update && apt-get install -y cargo")

def build(ctx):
    ctx.checkout()
    ctx.shell("cargo build --release")
    ctx.upload("target/release/myapp", artifact="bin")

def test(ctx):
    # test knows nothing about build — only that it needs an artifact.
    # that artifact could have been built right here, come from cache
    # or a registry, or even uploaded from a developer's laptop
    ctx.download("bin", dest=".")
    ctx.shell("cargo test")

def ci(ctx):
    src = ctx.source()
    b = ctx.run(build, setup=setup, source=src, image="mirum/ubuntu-24.04")
    ctx.run(test, setup=setup, depends=b, image="mirum/ubuntu-24.04")

def project(ctx):
    ctx.pipeline("ci", fn=ci, watch=ctx.watch(events=["push"]))
```

### Example: Cross-Platform Project

```python
# tasks/setup.star
def cpp_toolchain(ctx):
    if ctx.os == "linux":
        ctx.shell("apt-get update && apt-get install -y cmake ninja-build")
    elif ctx.os == "freebsd":
        ctx.shell("pkg install -y cmake ninja")
    elif ctx.os == "windows":
        ctx.shell("choco install -y cmake ninja visualstudio2022-workload-vctools")
    elif ctx.os == "macos":
        ctx.shell("brew install cmake ninja")

# tasks/build.star
def build(ctx):
    ctx.checkout()
    if ctx.os == "windows":
        ctx.shell('cmake -G "Visual Studio 17 2022" -B build .')
    elif ctx.os == "macos":
        ctx.shell("cmake -B build -DCMAKE_OSX_DEPLOYMENT_TARGET=12.0 .")
    else:
        ctx.shell("cmake -B build .")
    ctx.shell("cmake --build build --config Release")
    ctx.upload("build/out/*", artifact="pkg")

def publish(ctx):
    ctx.download("pkg", dest="release/")
    ctx.shell("gh release create $TAG release/* --generate-notes")

# pipelines/release.star
load("//tasks/setup.star", "cpp_toolchain")
load("//tasks/build.star", "build", "publish")

IMAGES = {
    "linux": "mirum/ubuntu-24.04",
    "freebsd": "mirum/freebsd-14",
    "windows": "mirum/windows-2025",
    "macos": "mirum/macos-15",
}

# Irregular matrix — just a list. No exclude needed.
PLATFORMS = [
    ("linux", "amd64"),
    ("linux", "arm64"),
    ("macos", "arm64"),
    ("windows", "amd64"),
    ("freebsd", "amd64"),
]

def pipeline(ctx):
    src = ctx.source(ref=ctx.event.tag)

    # Build: loop over platforms, each task gets its own handle
    builds = {}
    for os, arch in PLATFORMS:
        builds[(os, arch)] = ctx.run(build,
            setup=cpp_toolchain,
            source=src,
            image=IMAGES[os],
            args={"os": os, "arch": arch})

    # Publish: fan-in, waits for all builds
    ctx.run(publish, depends=list(builds.values()))

# .mirum/main.star
def project(ctx):
    ctx.pipeline("release", file="pipelines/release.star",
        watch=ctx.watch(events=["tag"], pattern="v*"))
```

The pipeline decides WHERE (image, platforms). Setup decides WITH WHAT
(toolchain, cached snapshot). The task decides HOW (checkout, build, upload).
Tasks don't know what platform they're running on — `ctx.os` and `ctx.arch` are
injected by the pipeline.

## Images

The most tedious and time-consuming task is preparing images for various operating
systems. Some distributions distribute qcow2, some support cloud-init, and some only
offer an ISO installer. Some require a network connection for configuration, while
others work offline.

Another problem is distribution. The reason containers are popular is the OCI registry.
A container is easy to upload to a server, and just as easy to download and deploy.
Nothing similar exists for VMs.

An elegant solution was found in Tart by CirrusCI: use OCI as a black box for storing
the VM image. Load it into your existing infrastructure, easily update, and distribute.
A single distribution format for all platforms. Images are stored as compressed
raw disk chunks:

```
OCI Image Manifest:
  config:
    mediaType: "application/vnd.mirum.image.config.v1+json"
    { mirum.version, os, arch, distro, distro_version,
      agent_version, disk_size, chunk_size }

  layers:
    - mediaType: "application/vnd.mirum.disk.raw.v1+zstd"
      annotations: { "mirum.offset": "0", "mirum.length": "67108864" }
    - ...

  # macOS additionally:
    - mediaType: "application/vnd.mirum.aux.v1+zstd"
    - mediaType: "application/vnd.mirum.hwmodel.v1+json"
```

Each chunk is independently zstd-compressed. The worker downloads and decompresses
in parallel. 64MB chunks for a 4GB disk ≈ 64 layers. On pull worker reassembles
raw disk from chunks → converts to hypervisor format
(qcow2, vhdx, Vz native) → caches → CoW clone per task.

### Three Layers (VM Runtime)

VM images are larger than container images, but there are fewer of them.
We can borrow the layer caching idea and apply it to image snapshots (like in qcow2+).

```
Layer 0: Base image (from OCI registry)
  Golden (Mirum-maintained) or organization (external).
  Worker downloads, converts to hypervisor format, caches locally.

Layer 1: Setup (function from ctx.run(setup=...))
  Declared in the pipeline. Worker executes, takes a snapshot.
  Cache is local, best-effort, evicted by LRU.

Layer 2: Ephemeral overlay
  CoW clone of setup cache (or base). Per-task. Destroyed.
```

### Setup as a Function

The pipeline passes two functions to `ctx.run()`:
`setup` (optional) and the main task. The image is also specified in the pipeline:

```python
def cpp_setup(ctx):
    if ctx.os == "linux":
        ctx.shell("apt-get update && apt-get install -y cmake ninja-build")
    elif ctx.os == "freebsd":
        ctx.shell("pkg install -y cmake ninja")

def build(ctx):
    ctx.checkout()
    ctx.shell("cmake -B build . && cmake --build build")

def pipeline(ctx):
    ctx.run(build, setup=cpp_setup, source=ctx.source(),
        image="mirum/ubuntu-24.04",
        args={"os": "linux"})
```

The worker hashes `(image_digest, setup_function_hash, os, arch)`.
Cache hit → CoW clone, boot in milliseconds. Miss → boot base, run setup,
snapshot, cache.

Setup is an ordinary Starlark function, composable via `load()`:

```python
# @pkg//acme/setup.star
def cpp_toolchain(ctx):
    if ctx.os == "linux":
        ctx.shell("apt-get update && apt-get install -y cmake ninja-build")
    elif ctx.os == "windows":
        ctx.shell("choco install -y cmake ninja")
```

```python
load("@pkg//acme/setup.star", "cpp_toolchain")

def pipeline(ctx):
    for os in ["linux", "windows"]:
        ctx.run(build, setup=cpp_toolchain, source=src,
            image=IMAGES[os], args={"os": os})
```

One `cpp_toolchain` across the entire organization — one hash — one snapshot per worker.

Three levels, cleanly separated:

- **Pipeline**: WHERE (image, platforms, source)
- **Setup**: WITH WHAT (toolchain, dependencies — cached snapshot)
- **Task**: HOW (checkout, build, test, upload)

Organizations that need a fully pre-built image can publish it to any OCI registry
using external tooling and reference it directly:

```python
# not need setup for preconfigured image
ctx.run(build, source=src, image="acme-registry.com/ci-base:latest")
```

### Golden Images

Golden images are built by the Mirum team using Packer, not by users:

| Platform                  | Packer builder         | Install method                      |
| ------------------------- | ---------------------- | ----------------------------------- |
| Linux (Ubuntu, Fedora...) | `qemu`                 | cloud-init / preseed / kickstart    |
| macOS                     | `tart` (Packer plugin) | VZMacOSInstaller + VNC boot_command |
| Windows                   | `qemu`                 | autounattend.xml (evaluation ISO)   |
| NetBSD                    | `qemu`                 | sysinst auto                        |
| FreeBSD                   | `qemu`                 | bsdinstall scripted                 |
| OpenBSD                   | `qemu`                 | autoinstall response file           |

Windows: evaluation ISO is freely downloadable. The evaluation period (180 days)
is irrelevant for ephemeral CI VMs. Users activate with their own key if needed.

macOS: `.ipsw` installed via Virtualization.framework. Setup Assistant automated
via VNC keystroke injection. Requires Apple hardware for building and running.

## Extensibility

Starlark simplifies building plugins and libraries. You already have `load`,
so you don't need to invent your own systems like reusable actions.

**Transparent worker optimizations** — invisible to the task.
Configured in `worker.yaml`. The worker configures the VM environment before running
any scripts: apt mirror, cargo/npm cache mount, HTTP proxy.
`./ci.sh` with `apt-get install` inside simply runs faster.
Bash scripts speed up for free.

```yaml
# worker.yaml
optimizations:
  apt_mirror: "http://apt-cache.internal:3142"
  http_proxy: "http://squid.internal:3128"
  cargo_cache: "/mnt/shared/cargo"
```

**Starlark stdlib (@mirum//)** — for things that require an explicit decision.
This is a standard Starlark that, although it comes with an agent,
doesn't require any additional APIs (see the Bazel vs Buck configurations).
Users can read, fork, or write their own.

Starlark sees capabilities via `ctx.worker.has("docker")`.
The stdlib adapts. No hidden magic — the code is readable.

The dividing principle: if an optimization can be applied without changing
task behavior — it's a transparent worker optimization. If the task needs to know
(e.g. a postgres address) — it's Starlark stdlib with graceful degradation.

Checks worker capabilities and adapts:

```python
load("@mirum//services", "service")

def test(ctx):
    # docker on the worker? → sidecar container
    # no docker? → install and run inside the VM
    service(ctx, "postgres", image="postgres:16", port=5432)
    ctx.shell("make test")
```

Workers declare capabilities on registration:

```yaml
# worker.yaml
capabilities:
  kvm: true
  gpu: false
  docker: true
```

**Server plugins (traits)** — extend the platform. Implement trait interfaces.
Configured in `server.yaml`:

| Trait            | AGPL built-in | External plugin        |
| ---------------- | ------------- | ---------------------- |
| AuthBackend      | Token, basic  | SAML, OIDC, LDAP       |
| Source provider  | Git, VSC, s3  | Mercurial, Perforse    |
| SecretProvider   | Env, systemd  | Vault, AWS KMS         |
| NotificationSink | —             | Slack, email, webhooks |
| BillingHook      | Noop          | Usage metering         |

## Comparison

We were inspired by many wonderful tools
- Buildbot: centralized master, property model, `try` for pre-commit testing, dynamic build steps;
- TeamCity: vsc roots, multi-tenant, role model, breadth of tool support; 
- Concourse: the idea of universal input/output;
- SourceHut: SSH into VMs for debugging, BSD support;
- Cirrus CI: ephemeral VMs, bring-your-own cloud, Starlark, agent inside VM;
- GitHub Actions: how not to do it.

### vs GitHub Actions

Paid, closed, inseparable from Microsoft, only ubuntu/windows/macOS,
only x64/arm64, nodejs required in runtime,
yaml configs (and only for the current repository).
No cross-OS matrices out of the box (macOS runners are a paid add-on).
No local execution. No dynamic DAG. Caching is an action, not a primitive.

### vs GitLab CI

GitLab CI is part of GitLab. YAML. DAG via `needs:` — a hack on top of a stage-based model.
Runners are stateful machines or Docker. No VM isolation. `include:` / YAML anchors — fragile reuse.
Dynamic child pipelines — via YAML generation.

### vs Jenkins

Groovy DSL is powerful but allows arbitrary code (RCE when eval'ing PRs).
Plugin ecosystem is huge but fragile (Security Advisories every month).
Agents are stateful, workspace persists. Shared Libraries are Groovy classes, trusted/untrusted.

### vs TeamCity

Powerful and popular, with clever ideas (dedicated VCS root, for example).
But it's paid and expensive, closed-source, and difficult to use.
Kotlin DSL is typed with IDE support, but allows side effects (HTTP, filesystem).
Agents are stateful and require maintenance. Snapshot dependencies equal our source consistency.

Templates (1:1) vs our `load()` (N:N) — Starlark is strictly more powerful.

### vs Buildbot

The most portable and flexible of all. However, it's outdated, difficult to configure,
and designed for hosted builds. Its architecture doesn't support SaaS.
Pure Python configurations on both the master and agents. No IaC out of the box.

### vs Cirrus CI

Great tool, but unfortunately still closed source and dependent on gcloud.
Starlark is only available as an advanced mode with yaml.
It lacks support for many BSDs (but FreeBSD is available!).

## Licensing

**AGPL-3.0** — all four modules, all built-in traits, all runtimes, standard library,
full CLI, basic Web UI, SQLite, single-tenant auth.

**Commercial license** — For companies unwilling to use the AGPL, offer a commercial
license, certifications, and SLAs in SaaS. Don't hesitate to take money
from enterprises and spend it on open source.
