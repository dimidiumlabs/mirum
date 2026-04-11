# Mirum Whitepaper

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

Evaluates pipeline Starlark (coroutine model), starts a VM for each
task, runs the task function inside, and returns the results to the
server. If a pipeline yields on a task result it is not yet ready for,
the worker leaves the suspended coroutine in the queue and resumes it
when the awaited task completes.

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
Contains a Starlark runtime for local eval and embeds a host worker for
in-process execution. Takes the server's role locally.

Technically, it is a lightweight disposable server that runs a real worker locally
to replicate real-world conditions in a cluster as closely as possible.

- `mirum task <name>`: run a single registered task on the host, no VM.
- `mirum run <pipeline>`: eval pipeline → spawn worker → dispatch tasks → display logs.
- `mirum list`: list registered tasks and pipelines.
- `mirum try <pipeline>`: send a local diff to the server.
- `mirum ssh <vm>`: shell into a failed VM.
- `mirum eval <pipeline>`: show the DAG without running anything.

## Configuration Model

Mirum operates on projects, a project consists of pipelines,
pipelines consist of tasks. A task is the minimum unit of execution — it always
runs in a single VM. Pipelines are DAGs (directed acyclic graphs) that invoke multiple
tasks and pass state between them. A project tracks a list of pipelines, their trigger
rules (watch, cron, manual, ...), and result notifications.

A quick example, all in one starlark file:

```python
# /Mirumfile — the only file required

load("@mirum//on.star", "git")

# A single pipeline describes a matrix of multiple operating systems and architectures.
# A single Linux host with KVM serves Linux, Windows, and BSD guests.
# A macOS host serves macOS, Linux, and BSD.
# Tasks adapt to the platform via `tctx.os` — they don't choose it.

# pctx.run takes an optional setup function to indicate which steps
# are environment setup only, so the resulting image can be cached
def setup(tctx):
    tctx.shell("apt-get update && apt-get install -y cargo")

def build(tctx):
    tctx.checkout()
    tctx.shell("cargo build --release")
    tctx.upload("target/release/myapp", artifact="bin")
task(build)

def test(tctx):
    # test knows nothing about build — only that it needs an artifact.
    # that artifact could have been built right here, come from cache
    # or a registry, or even uploaded from a developer's laptop
    tctx.download("bin", dest=".")
    tctx.shell("cargo test")
task(test)

# A pipeline is a function that dispatches tasks. It is registered the
# same way tasks are, with a list of triggers.
def ci(pctx):
    b = pctx.run(build, setup=setup, image="mirum/ubuntu-24.04")
    pctx.run(test, setup=setup, depends=b, image="mirum/ubuntu-24.04")
pipeline(ci, on=[git.push(branches=["main"])])
```

### Functions All the Way Down

Mirum configuration is nested function composition in Starlark. There
are two kinds of registered things — **tasks** and **pipelines** — and
both are registered as side effects of top-level calls in the file:

```python
def build(tctx):
    ...
task(build)

def ci(pctx):
    ...
pipeline(ci, on=[git.push(branches=["main"])])
```

A pipeline is a function that imperatively dispatches tasks and passes
dependencies between them. A task's result can be used to launch further
tasks, enabling dynamic task creation. There is no separate syntax for
"allow failure / retry / skip_if / matrix" — it is just `if`/`for` in
Starlark.

```
Mirumfile      → the file the server looks for (one per repo, at root)

triggers       → event routing          "when to run"
                 (predicates passed via pipeline(..., on=[...]))

pipeline(pctx) → DAG of tasks           "what to run, on which platforms"

task(tctx)     → scripts + artifacts    "how to build"
```

Starlark supports imports, so splitting a large project works out of the
box. The only requirement is a `Mirumfile` at the repository root.

The server reads `Mirumfile` (and any files it transitively `load()`s)
via the forge contents API, not via git clone. This allows: fetching
only configuration files without accessing source code, filtering
webhooks (push with no changes in `Mirumfile`'s closure → skip re-eval),
caching configuration by file SHAs.

To reuse code both within and outside the project, Starlark `load` is used.

```
@mirum//   Standard library (triggers, services, apt, bazel helpers)
@pkg//     External packages (from deps.star, pinned by commit)
//         Local files (relative to repo root)
```

### Coroutine Eval: Dynamic DAGs

Pipeline functions execute as coroutines. `pctx.run()` without accessing
results is non-blocking — the server accumulates pending tasks.
Accessing a result (`.output()`) is a yield point: the server dispatches
all pending tasks, waits for the needed result, and resumes the pipeline
function.

```python
def build(tctx):
    ...
task(build)

def discover_tests(tctx):
    ...
task(discover_tests)

def run_test(tctx, module):
    ...
task(run_test)

def ci(pctx):
    # All pctx.run() calls before the first .output() accumulate and run in parallel
    builds = {}
    for os in ["linux", "mac"]:
        builds[os] = pctx.run(build,
            image="mirum/%s" % os, args={"os": os})

    discovery = pctx.run(discover_tests, image="mirum/linux")

    # Yield: server dispatches builds + discovery in parallel,
    # waits for discovery to complete, resumes with result
    test_modules = discovery.output("modules")

    # Dynamic phase: use runtime data
    for module in test_modules:
        pctx.run(run_test, args={"module": module},
            depends=list(builds.values()))
pipeline(ci, on=[git.push(branches=["main"])])
```

If a pipeline function never calls `.output()`, the entire DAG is built
in a single pass. A static DAG is a special case of the dynamic model.

`mirum eval` executes the pipeline function locally without dispatching
tasks. For static DAGs it prints the full graph. For dynamic DAGs —
everything up to the first yield point, marked "depends on runtime data
beyond this point."

### Example: Everything in One File

```python
# /Mirumfile — complete CI

load("@mirum//on.star", "git")

# pctx.run takes an optional setup function to indicate which steps
# are environment setup only, so the resulting image can be cached
def setup(tctx):
    tctx.shell("apt-get update && apt-get install -y cargo")

def build(tctx):
    tctx.checkout()
    tctx.shell("cargo build --release")
    tctx.upload("target/release/myapp", artifact="bin")
task(build)

def test(tctx):
    # test knows nothing about build — only that it needs an artifact.
    # that artifact could have been built right here, come from cache
    # or a registry, or even uploaded from a developer's laptop
    tctx.download("bin", dest=".")
    tctx.shell("cargo test")
task(test)

def ci(pctx):
    b = pctx.run(build, setup=setup, image="mirum/ubuntu-24.04")
    pctx.run(test, setup=setup, depends=b, image="mirum/ubuntu-24.04")
pipeline(ci, on=[git.push(branches=["main"])])
```

### Example: Cross-Platform Project

```python
# tasks/setup.star
def cpp_toolchain(tctx):
    if tctx.os == "linux":
        tctx.shell("apt-get update && apt-get install -y cmake ninja-build")
    elif tctx.os == "freebsd":
        tctx.shell("pkg install -y cmake ninja")
    elif tctx.os == "windows":
        tctx.shell("choco install -y cmake ninja visualstudio2022-workload-vctools")
    elif tctx.os == "macos":
        tctx.shell("brew install cmake ninja")

# tasks/build.star
def build(tctx):
    tctx.checkout()
    if tctx.os == "windows":
        tctx.shell('cmake -G "Visual Studio 17 2022" -B build .')
    elif tctx.os == "macos":
        tctx.shell("cmake -B build -DCMAKE_OSX_DEPLOYMENT_TARGET=12.0 .")
    else:
        tctx.shell("cmake -B build .")
    tctx.shell("cmake --build build --config Release")
    tctx.upload("build/out/*", artifact="pkg")
task(build)

def publish(tctx):
    tctx.download("pkg", dest="release/")
    tctx.shell('gh release create "$TAG" release/* --generate-notes')
task(publish)

# /Mirumfile
load("@mirum//on.star", "git")
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

def release(pctx):
    # Build: loop over platforms, each task gets its own handle
    builds = {}
    for os, arch in PLATFORMS:
        builds[(os, arch)] = pctx.run(build,
            setup=cpp_toolchain,
            image=IMAGES[os],
            args={"os": os, "arch": arch})

    # Publish: fan-in, waits for all builds
    pctx.run(publish, depends=list(builds.values()))
pipeline(release, on=[git.tag(names=["v*"])])
```

The pipeline decides WHERE (image, platforms). Setup decides WITH WHAT
(toolchain, cached snapshot). The task decides HOW (checkout, build, upload).
Tasks don't know what platform they're running on — `tctx.os` and `tctx.arch`
are injected by the pipeline.

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

Layer 1: Setup (function from pctx.run(setup=...))
  Declared in the pipeline. Worker executes, takes a snapshot.
  Cache is local, best-effort, evicted by LRU.

Layer 2: Ephemeral overlay
  CoW clone of setup cache (or base). Per-task. Destroyed.
```

### Setup as a Function

The pipeline passes two functions to `pctx.run()`:
`setup` (optional) and the main task. The image is also specified in the pipeline:

```python
def cpp_setup(tctx):
    if tctx.os == "linux":
        tctx.shell("apt-get update && apt-get install -y cmake ninja-build")
    elif tctx.os == "freebsd":
        tctx.shell("pkg install -y cmake ninja")

def build(tctx):
    tctx.checkout()
    tctx.shell("cmake -B build . && cmake --build build")
task(build)

def ci(pctx):
    pctx.run(build, setup=cpp_setup,
        image="mirum/ubuntu-24.04",
        args={"os": "linux"})
pipeline(ci, on=[git.push(branches=["main"])])
```

The worker hashes `(image_digest, setup_function_hash, os, arch)`.
Cache hit → CoW clone, boot in milliseconds. Miss → boot base, run setup,
snapshot, cache.

Setup is an ordinary Starlark function, composable via `load()`:

```python
# @pkg//acme/setup.star
def cpp_toolchain(tctx):
    if tctx.os == "linux":
        tctx.shell("apt-get update && apt-get install -y cmake ninja-build")
    elif tctx.os == "windows":
        tctx.shell("choco install -y cmake ninja")
```

```python
load("@pkg//acme/setup.star", "cpp_toolchain")

def ci(pctx):
    for os in ["linux", "windows"]:
        pctx.run(build, setup=cpp_toolchain,
            image=IMAGES[os], args={"os": os})
```

One `cpp_toolchain` across the entire organization — one hash — one snapshot per worker.

Three levels, cleanly separated:

- **Pipeline**: WHERE (image, platforms)
- **Setup**: WITH WHAT (toolchain, dependencies — cached snapshot)
- **Task**: HOW (checkout, build, test, upload)

Organizations that need a fully pre-built image can publish it to any OCI registry
using external tooling and reference it directly:

```python
# no need for setup with a preconfigured image
pctx.run(build, image="acme-registry.com/ci-base:latest")
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

Starlark sees capabilities via `tctx.worker.has("docker")`.
The stdlib adapts. No hidden magic — the code is readable.

The dividing principle: if an optimization can be applied without changing
task behavior — it's a transparent worker optimization. If the task needs to know
(e.g. a postgres address) — it's Starlark stdlib with graceful degradation.

Checks worker capabilities and adapts:

```python
load("@mirum//services", "service")

def test(tctx):
    # docker on the worker? → sidecar container
    # no docker? → install and run inside the VM
    service(tctx, "postgres", image="postgres:16", port=5432)
    tctx.shell("make test")
task(test)
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
