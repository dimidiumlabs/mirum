# Mirumfile

> [!CAUTION]
> This document is the API reference for the `Mirumfile` format. The
> implementation is in progress and the runtime does not yet match this
> document.

`Mirumfile` is a single Starlark file at the repository root. It defines
**tasks** (units of work, run on a host or in a VM) and **pipelines**
(graphs of tasks, scheduled across one or more VMs). The same file is
read by the local `mirum` CLI and by mirum-server in a cluster.

## CLI

```
mirum task <name> [args...]   Run a registered task. Positional args after
                              <name> become positional arguments to the
                              task function after tctx:
                                  mirum task build linux amd64
                                  → build(tctx, "linux", "amd64")

mirum run <pipeline>          Run a registered pipeline.
```

## File format and discovery

`Mirumfile` lives at the repository root. This is the only required file
for Mirum.

The file is standard Starlark with the following predeclared globals:

| Name        | Kind                | Purpose                              |
|-------------|---------------------|--------------------------------------|
| `task`      | builtin function    | Register a task                      |
| `pipeline`  | builtin function    | Register a pipeline                  |
| `fail`      | standard Starlark   | Abort with a message                 |
| `print`     | standard Starlark   | Write to the task log                |
| `struct`    | standard Starlark   | Build anonymous records              |

Multi-file projects use standard Starlark `load()`:

```python
load("//tasks/build.star", "build", "test")
load("//tasks/release.star", "package")
```

The mirum standard library is mounted under `@mirum//`:

```python
load("@mirum//on.star", "git", "cron", "any_of")
load("@mirum//pkg.star", "install")
```

## Naming convention

By convention, the first parameter of a function is named according to the
ctx kind it expects:

- `tctx` for task functions and helpers that operate on a task ctx
- `pctx` for pipeline functions and helpers that operate on a pipeline ctx
- `event` for trigger predicates

This convention is not enforced by the runner — it is just a readability
aid. A reader of `def lint(tctx):` immediately knows it expects a task
ctx.

## Registration

Tasks and pipelines are registered as **side effects** of top-level
calls, not as values bound to names. The function is defined first, then
registered:

```python
def build(tctx):
    """Build mirum-server"""
    tctx.exec(["go", "build", "-o", "build/mirum-server", "./cmd/mirum-server"])
task(build)

def test(tctx):
    """Run tests"""
    tctx.need(build)
    tctx.exec(["go", "test", "-race", "-count=1", "./..."])
task(test)

load("@mirum//on.star", "git")

def ci(pctx):
    """CI: build + test on Linux"""
    pctx.run(test, image="mirum/ubuntu-24.04")
pipeline(ci, on=[git.push(branches=["main"])])
```

The function being registered remains an ordinary callable. It can be
called directly (`build(tctx)`), passed as a value (`pctx.run(test, ...)`),
and referenced by `tctx.need`. Registration is metadata for the CLI and
the server — it does not wrap or replace the function.

A function defined at the top level that is **not** registered is a
**helper**: invisible to `mirum list`, not invocable as `mirum task`,
no special calling convention. Helpers are just regular functions called
from registered ones. There is no `_` prefix convention.

```python
def check_gofmt(tctx):                       # helper, not registered
    r = tctx.exec(["gofmt", "-l", "."], check=False)
    if r.stdout.strip():
        fail("gofmt: needs formatting:\n" + r.stdout)

def lint(tctx):
    """Run static checks"""
    tctx.exec(["go", "vet", "./..."])
    check_gofmt(tctx)
task(lint)
```

### `task(fn, name=None)`

Registers `fn` as a task. The default name is the Starlark function name;
the optional `name` keyword overrides it. Returns `None`. Re-registration
of the same name is a hard error at eval time.

### `pipeline(fn, name=None, on=None)`

Registers `fn` as a pipeline. Same naming rules as `task`. The `on`
parameter is a single predicate or a list of predicates that decide when
mirum-server triggers the pipeline (see [Triggers](#triggers)). Returns
`None`.

## Triggers

A trigger is a **predicate function**: it takes an event and returns a
bool. mirum-server runs each registered pipeline whose `on=` predicate
returns True for an incoming event. With a list, the pipeline runs if
**any** predicate matches (OR semantics).

Predicates are ordinary Starlark functions, defined either by the user or
by the standard library. There is no separate trigger DSL.

### Writing predicates by hand

```python
def main_push(event):
    return (event.kind == "git"
            and event.type == "push"
            and event.branch == "main")

def go_changes(event):
    if event.kind != "git":
        return False
    if event.type != "push" and event.type != "pull_request":
        return False
    return any([p.endswith(".go") or p == "go.mod" for p in event.paths])

def by_release_bot(event):
    return (event.kind == "git"
            and event.type == "push"
            and event.author == "release-bot")

def ci(pctx):
    ...
pipeline(ci, on=[main_push, go_changes])
```

### Standard library factories

For common cases the `@mirum//on.star` stdlib provides predicate
factories — Starlark functions that return predicates. They are
themselves written in plain Starlark; users can fork the file or write
their own factories the same way. Factories for source-specific events
are grouped by source kind, exposed as structs:

```python
load("@mirum//on.star", "git", "cron", "manual", "any_of", "all_of")

def ci(pctx):
    ...
pipeline(ci, on=[
    git.push(branches=["main"], paths=["**.go", "go.mod"]),
    git.pull_request(branches=["main"]),
])

def release(pctx):
    pctx.run(build_release, image="mirum/ubuntu-24.04")
pipeline(release, on=[git.tag(names=["v*"])])

def nightly(pctx):
    ...
pipeline(nightly, on=[cron("0 6 * * *")])
```

A factory is just a closure-returning function — there is nothing
special about it on the runtime side. A trimmed version of the stdlib's
`git.push`:

```python
# @mirum//on.star
def _git_push(branches=None, paths=None):
    def predicate(event):
        if event.kind != "git" or event.type != "push":
            return False
        if branches and event.branch not in branches:
            return False
        if paths and not _any_glob(event.paths, paths):
            return False
        return True
    return predicate

git = struct(
    push = _git_push,
    tag = _git_tag,
    pull_request = _git_pull_request,
)
```

When a non-git source is added — Perforce, Mercurial, Subversion — its
factories live in their own namespace alongside `git`:

```python
load("@mirum//on.star", "git", "perforce")

pipeline(ci, on=[
    git.push(branches=["main"]),
    perforce.submit(branches=["//depot/main/..."]),
])
```

User-written factories compose with stdlib ones identically. To require
that a build runs only when both `main` is pushed AND a release-bot is
the author, mix stdlib and hand-written predicates with `all_of`:

```python
load("@mirum//on.star", "git", "all_of")

def by_release_bot(event):
    return (event.kind == "git"
            and event.type == "push"
            and event.author == "release-bot")

def bot_release(pctx):
    ...
pipeline(bot_release, on=[
    all_of(git.push(branches=["main"]), by_release_bot),
])
```

### `EventCtx`

The single argument passed to every predicate. Same value is also
available inside pipeline functions as `pctx.event`. EventCtx is
read-only.

Only two fields are guaranteed on every event:

| Field             | Type | Meaning                                |
|-------------------|------|----------------------------------------|
| `event.kind`      | str  | Source family that produced the event: `"git"`, `"perforce"`, `"hg"`, `"cron"`, `"manual"`, `"s3"`, `"webhook"`, … |
| `event.timestamp` | int  | Unix epoch seconds of when the event occurred |

**Everything else depends on `event.kind`.** Each kind documents its
own fields. Source families that have multiple distinct event types
(e.g. `git` has push, tag, pull_request) also expose `event.type` as a
sub-discriminator.

A predicate that wants to read kind-specific fields **must check
`event.kind` first** (and `event.type` if the kind has multiple types).
Accessing a field that does not exist for the current event raises an
error. Predicates that do not recognize a kind should return `False`,
not crash. New event kinds and types may be added at any time; existing
predicates that check for known kinds remain valid.

**`kind == "manual"`**

| Field            | Type        | Meaning                                |
|------------------|-------------|----------------------------------------|
| `event.user`     | str         | Identifier of the user who triggered   |
| `event.reason`   | str \| None | Optional reason supplied by the user   |

**`kind == "cron"`**

| Field            | Type | Meaning                                     |
|------------------|------|---------------------------------------------|
| `event.schedule` | str  | The cron expression that fired              |

**`kind == "git"`**

Fields common to all git events:

| Field            | Type | Meaning                                          |
|------------------|------|--------------------------------------------------|
| `event.type`     | str  | `"push"`, `"tag"`, or `"pull_request"`           |
| `event.source`   | str  | Name of the source set                           |
| `event.commit`   | str  | Commit SHA                                       |
| `event.author`   | str  | Author / committer / tagger / PR author          |

Additional fields by `event.type`:

`type == "push"`:

| Field            | Type      | Meaning                                |
|------------------|-----------|----------------------------------------|
| `event.branch`   | str       | Branch pushed to                       |
| `event.message`  | str       | Commit message                         |
| `event.paths`    | list[str] | Paths changed by the push              |

`type == "tag"`:

| Field            | Type | Meaning                                          |
|------------------|------|--------------------------------------------------|
| `event.tag`      | str  | Tag name                                         |
| `event.message`  | str  | Tag annotation, if any                           |

`type == "pull_request"`:

| Field            | Type      | Meaning                                |
|------------------|-----------|----------------------------------------|
| `event.number`   | int       | PR number                              |
| `event.title`    | str       | PR title                               |
| `event.base`     | str       | Target branch                          |
| `event.head`     | str       | Source branch                          |
| `event.draft`    | bool      | Draft state                            |
| `event.labels`   | list[str] | Labels currently applied               |
| `event.paths`    | list[str] | Paths changed in the PR                |

Additional event kinds (other VCS systems, S3 object events, generic
webhooks, file watchers, external systems) bring their own field sets
and are documented as they are added. Existing predicates are
unaffected: they match the kinds they know and return `False` for
everything else.

## TaskCtx

The argument passed to every task function. The only ctx that can
execute commands.

### Execution

```
tctx.shell(script, **opts) -> Result
tctx.exec(argv, **opts)    -> Result
```

`shell` runs `script` through a POSIX/bash interpreter with full support
for pipes, redirects, command substitution, here-docs, variable expansion,
and control flow. Same shell semantics on every platform; no `/bin/bash`
dependency.

`exec` runs a single command without shell parsing. Use it when you have
an argv list and want zero shell interpretation.

Common keyword options for both:

| Option     | Default     | Meaning                                                   |
|------------|-------------|-----------------------------------------------------------|
| `env`      | `{}`        | Additional environment variables (added to `tctx.env`)    |
| `cwd`      | `None`      | Working directory (relative to current `tctx.cwd`)        |
| `stdin`    | `None`      | None / str / bytes / path                                 |
| `stdout`   | `None`      | None (capture+stream) / path / `DEVNULL`                  |
| `stderr`   | `None`      | None (capture+stream) / path / `DEVNULL` / `STDOUT`       |
| `append`   | `False`     | Open `stdout` / `stderr` paths in append mode             |
| `timeout`  | `None`      | Seconds; kill on expiry                                   |
| `check`    | `True`      | Non-zero exit raises `fail()` automatically               |
| `capture`  | `True`      | Populate `Result.stdout` / `Result.stderr`                |

`Result` is a struct value with attributes:

```python
result.stdout   # str
result.stderr   # str
result.code     # int
result.ok       # bool (code == 0)
```

`check=True` is the default — non-zero exits abort the task. Tasks that
want to inspect the exit code use `check=False`:

```python
r = tctx.exec(["test", "-f", path], check=False)
if r.ok:
    ...
```

### Immutable derive

```
tctx.with_env({"K": "V"}) -> tctx
tctx.with_cwd("subdir")   -> tctx
```

Both return a new ctx with the modification applied. The original ctx is
unchanged.

### Introspection

```
tctx.cwd     # str — current working directory (absolute)
tctx.env     # dict-like — current environment
tctx.os      # "linux" | "darwin" | "freebsd" | "windows" | ...
tctx.arch    # "amd64" | "arm64" | "riscv64" | ...
```

### `tctx.need(fn, *args, **kwargs) -> Result | None`

Runs `fn(tctx, *args, **kwargs)` if it has not already been called with
the same arguments in this invocation; otherwise returns the cached
result. Use it for "make sure this happened" semantics; use a direct
call (`build(tctx)`) for "definitely run this now" semantics.

Dedup key is `(fn, args, kwargs)`. Different arguments to the same
function are different invocations:

```python
def build(tctx, goos="linux", goarch="amd64"):
    ...

def all_platforms(tctx):
    for goos, goarch in [("linux", "amd64"), ("darwin", "arm64")]:
        tctx.need(build, goos, goarch)   # two distinct invocations
```

### `tctx.checkout(name="main", ref=None) -> str`

Materializes a source set inside the task's environment and returns
the absolute path to it. The runtime is responsible for fetching the
right files; from the task's perspective the call is idempotent —
calling it again returns the same path without doing extra work.

When `ref` is omitted, the runtime picks the natural ref for the
triggering event:

| Event                       | Default ref               |
|-----------------------------|---------------------------|
| `git push`                  | The pushed commit         |
| `git tag`                   | The tagged commit         |
| `git pull_request`          | The PR head               |
| `cron`                      | Default branch            |
| `manual` / `mirum task`     | Current working tree      |

To use a different ref explicitly, pass `ref=` (a SHA, branch, or tag
name). Pipelines that need to override the default usually do so by
forwarding event data through `args`:

```python
def build_at(tctx, ref):
    src = tctx.checkout(ref=ref)
    ...
task(build_at)

def replay(pctx):
    pctx.run(build_at, image="...", args={"ref": pctx.event.commit})
```

Multiple source sets are accessed by name:

```python
def integration(tctx):
    src = tctx.checkout()                  # default source set
    fixtures = tctx.checkout("fixtures")   # named source set
    tctx.with_env({"FIXTURES": fixtures}).exec(["go", "test", "./tests/integration/..."])
```

Source set names are defined in [Source sets](#source-sets), not in
pipeline or task code. A task that needs source files should call
`tctx.checkout()` explicitly — both as documentation of intent and
because some workers may stage source lazily on first call.

### `tctx.upload(path, artifact="name")` and `tctx.download(name, dest=".")`

`upload` registers a file as a named artifact under the current task.
`download` retrieves a previously uploaded artifact by name into a
destination directory.

Locally, artifacts are tracked in `.mirum/local-artifacts.json` in the
repo root and persist between `mirum` invocations. In the cluster they
move through mirum-server. The task functions are unchanged either way.

`download` of an artifact that was never uploaded fails with a clear
error.

## PipelineCtx

The argument passed to every pipeline function. **Cannot execute shell
commands.** Pipeline code may come from untrusted PRs; it is restricted
to orchestration.

### `pctx.event`

Read-only [`EventCtx`](#eventctx) for the event that triggered this
pipeline. The same value that was passed to the trigger predicates.

When `mirum run` is invoked locally, the event is synthetic
(`{"kind": "manual"}` by default; overridable via CLI flags).

### `pctx.run(task_fn, image=, args={}, setup=None, depends=[]) -> handle`

Dispatches a task into a new VM. Returns a handle that can be passed as
`depends=[handle, ...]` to subsequent `pctx.run` calls.

| Argument   | Meaning                                                                  |
|------------|--------------------------------------------------------------------------|
| `task_fn`  | Task function to invoke (the function value, not the registered name)    |
| `image`    | OCI reference to the VM image                                            |
| `args`     | Keyword arguments forwarded to `task_fn(tctx, **args)`                   |
| `setup`    | Optional setup function for cached snapshots                             |
| `depends`  | Handles from prior `pctx.run` calls. This VM does not start until they finish, and inherits their `upload`'d artifacts |

Source materialization is a task-side concern, not a pipeline-side one
— see [`tctx.checkout`](#tctxcheckoutnamemain---str). The pipeline does
not pass source refs to its tasks; each task asks for the source sets
it needs by name, and the runner resolves them based on the triggering
event.

`depends=` expresses **VM topology**, not task dependency. Within a
single VM, task functions compose via `tctx.need(...)`. Across VMs, the
pipeline orchestrates via `pctx.run(..., depends=[...])`. These are two
distinct mechanisms and are not interchangeable.

Example: cross-platform release that fans out builds and fans in publish.

```python
load("@mirum//on.star", "git")

PLATFORMS = [
    ("linux",   "amd64"), ("linux",   "arm64"),
    ("darwin",  "arm64"),
    ("windows", "amd64"),
    ("freebsd", "amd64"),
]

IMAGES = {
    "linux":   "mirum/ubuntu-24.04",
    "darwin":  "mirum/macos-15",
    "windows": "mirum/windows-2025",
    "freebsd": "mirum/freebsd-14",
}

def release(pctx):
    builds = []
    for goos, goarch in PLATFORMS:
        builds.append(pctx.run(build,
            image=IMAGES[goos],
            args={"goos": goos, "goarch": goarch}))

    pctx.run(publish, image=IMAGES["linux"], depends=builds)
pipeline(release, on=[git.tag(names=["v*"])])
```

## Source sets

Source URLs, refs, auth, and repo locations are **configuration**.
Pipeline and task code reference source sets only by **name**.

In a cluster, source sets are configured per project in mirum-server's
WebUI. The server resolves names to URLs and credentials when staging a
VM, and credentials never reach the VM or the task code.

Locally, source sets are defined in `.mirum/sources.json` in the repo
root:

```json
{
  "main": ".",
  "fixtures": "/home/user/work/test-fixtures"
}
```

Values are absolute paths to local checkouts. The `main` entry can be
omitted; it defaults to the directory containing `Mirumfile`. The file
is opt-in for git tracking — projects with shared layout may commit it,
projects with user-specific paths typically gitignore it.

A `tctx.checkout("name")` for a name not in the configuration is a hard
error.

## Safe shell interpolation

Dynamic values are passed through the `env=` keyword and referenced as
quoted shell variables in the script body. This gives POSIX single-word
expansion semantics — the value becomes one literal argument and is
never re-parsed:

```python
# Even if branch == "; rm -rf /", this is safe — it is one literal arg
tctx.shell('git log --format=%H "$BRANCH"', env={"BRANCH": branch})
```

Never build shell scripts by string concatenation. Always pass dynamic
values through `env=`.

`tctx.exec(argv, ...)` bypasses the shell entirely; use it when you
already have an argv list and want zero chance of shell interpretation.
