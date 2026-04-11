# Mirum

> [!CAUTION]
> This document describes the design and rationale of the system. It does
> not reflect the current state — almost everything is still
> unimplemented. For the canonical user-facing API of the configuration
> file, see [`mirumfile.md`](./docs/mirumfile.md).

An experimental portable CI platform with VM-first isolation,
programmable pipelines, local execution parity and Starlark configs.

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

- `mirum task` — run a single task on the host without spinning up a VM,
  for fast iteration during development
- `mirum run` — run the pipeline locally with the same VMs but with a local copy
  of your code and a debugger
- `mirum ssh` — connect to a failed VM over SSH and debug on real hardware
- `mirum try` — run a build from local changes on the cluster without creating
  throwaway commits

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

## Installation

Please note that the project is in its infancy and is **not** intended for production use.

**Debian/Ubuntu:**

```bash
sudo apt install curl gnupg

curl -fsSL https://dl.mirum.dev/public.gpg | sudo gpg --dearmor -o /usr/share/keyrings/mirum.gpg
echo "deb [signed-by=/usr/share/keyrings/mirum.gpg] https://dl.mirum.dev/apt/ nightly main" | sudo tee /etc/apt/sources.list.d/mirum.list
sudo apt update && sudo apt install mirum

# Start the server
sudo systemctl enable --now mirum-server

# Start a worker (optional, can run on a different host)
sudo systemctl enable --now mirum-worker@default
```

**Fedora/RHEL:**

```bash
# DNF5 (Fedora 41+, RHEL 10+)
sudo dnf config-manager addrepo --from-repofile=https://dl.mirum.dev/rpm/nightly/mirum-nightly.repo

# DNF4 (Fedora 40 and older, RHEL 8/9)
sudo curl -o /etc/yum.repos.d/mirum-nightly.repo https://dl.mirum.dev/rpm/nightly/mirum-nightly.repo

sudo dnf install mirum

# Start the server
sudo systemctl enable --now mirum-server

# Start a worker (optional, can run on a different host)
sudo systemctl enable --now mirum-worker@default
```

**openSUSE:**

```bash
sudo rpm --import https://dl.mirum.dev/public.gpg
sudo zypper addrepo https://dl.mirum.dev/rpm/nightly/ mirum-nightly
sudo zypper refresh
sudo zypper install mirum

# Start the server
sudo systemctl enable --now mirum-server

# Start a worker (optional, can run on a different host)
sudo systemctl enable --now mirum-worker@default
```

## Contributing

We welcome your contributions, including code, bug reports, ideas, and success stories.

If you are making a contribution for the first time or from a new email,
please add yourself to the `.mailmap`.

### Contributor License Agreement

To include your code, we ask that you read and agree to the [CLA](./CLA.md).
To sign, add a `Signed-off-by` trailer to every commit (`git commit -s`).
Each commit in a pull request must carry a valid `Signed-off-by`
line matching the commit author. Please use your real name or your
public nickname. We cannot include code from anonymous contributors.

AI agents MUST NOT add Signed-off-by tags.
Only humans can legally certify the Contributor License Agreement.

### AI policy

You may use AI agents when writing code and documentation. AI is not allowed for
media including images, videos, fonts at all. You must fully read, understand,
and cleanup any code generated by the agent. We ask that you disclose the agent's
use and indicate the tool, model, and extent of contribution.

Contributions should include an Assisted-by tag in the following format:
`Assisted-by: AGENT_NAME:MODEL_VERSION [TOOL1] [TOOL2]`, for example:
`Assisted-by: Claude:claude-4.6-opus coccinelle sparse`

Remember, AI agents should make software better, not worse.

## License

Copyright (C) 2026 Nikolay Govorov

This program is free software: you can redistribute it and/or modify it under
the terms of the GNU Affero General Public License as published by the Free
Software Foundation, either version 3 of the License, or (at your option) any
later version.

This program is distributed in the hope that it will be useful, but WITHOUT ANY
WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS FOR A
PARTICULAR PURPOSE. See the GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License along
with this program. If not, see <https://www.gnu.org/licenses/>.
