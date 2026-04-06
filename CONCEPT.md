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

A quick example pipeline, all in one starlark file:

```python
# /.mirum/main.star — complete CI
# A single pipeline describes a matrix of multiple operating systems and architectures.
# A single Linux host with KVM serves Linux, Windows, and \*BSD guests. A macOS host serves macOS, Linux, and \*BSD.
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
