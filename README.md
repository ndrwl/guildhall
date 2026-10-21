# guildhall

[![CI](https://github.com/ndrwl/guildhall/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/ndrwl/guildhall/actions/workflows/ci.yml?query=branch%3Amain)
[![Go version](https://img.shields.io/github/go-mod/go-version/ndrwl/guildhall)](go.mod)
[![License](https://img.shields.io/github/license/ndrwl/guildhall)](LICENSE)

Agent-agnostic local engineering workflows, shared tooling and fleet setup.

Guildhall is at its foundation stage: it builds and runs, but no platform or agent runtime is supported for real work yet.
The specification is in `docs/requirements.md`.

The current foundation provides configuration and workflow parsing and validation, the durable record store and a local message inbox on it, and the Steward's managed-settings rollouts, not a runnable Director or workflow executor.
See [configuration and workflows](docs/configuration.md) for the format, examples and library usage, and [Steward rollouts](docs/steward.md) for rollouts.

## What the badges mean

The CI badge is the live result of the [CI workflow](.github/workflows/ci.yml) on `main`.
CI runs formatting, build, vet, tests and lint on Linux, Windows and macOS for every pull request and every push to `main`.
It makes no model calls and uses no credentials or network in tests.
Passing CI is not live acceptance: no platform or runtime is supported until its acceptance has passed there, and CI on macOS is not live macOS compatibility.
Model-powered review is separate: changes are also reviewed by an automated review and validation pipeline configured in `.no-mistakes.yaml`, which runs outside CI and is not reported by these badges.

## Design

Guildhall coordinates coding agents on the machines one person owns.
A single Go binary owns all state and lifecycle, so the agents it drives are interchangeable and a restart never depends on an agent's transcript.
This section describes the accepted design; [Inside a Director](#inside-a-director) marks which parts are built.

### Roles

Each role owns a fixed set of things; the next figure shows how they talk.

| Role | What it is | What it owns |
| --- | --- | --- |
| **Guildmaster** | The human who owns the guild | Answers to decisions and approvals |
| **Steward** | The shared maintenance agent | The private central preferences and the roster of enrolled machines |
| **Director** | The coordinator of one or more repositories on its one owner machine | Its tasks: it tracks them, launches Artisans and brings decisions to the Guildmaster, but never does the work itself |
| **Artisan** | A task worker launched by a Director | One task attempt in an isolated copy of its repository |
| Go **core** | The single Guildhall binary | All durable state and lifecycle: each Director copy has one store holding its tasks, attempts, decisions, document references, pane bindings and messages, so a restart never depends on an agent's transcript |

### Who talks to whom

The Guildmaster talks directly to the Steward and to each Director; there is no central dispatcher.
Each arrow shows one direction and what flows along it.

```mermaid
flowchart LR
    GM(["Guildmaster"])
    ST["Steward"]

    subgraph M1["Machine A"]
        DA["Director: tools"]
        AA1["Artisan"]
        AA2["Artisan"]
    end

    subgraph M2["Machine B"]
        DB["Director: web"]
        AB1["Artisan"]
    end

    GM -- "requests" --> ST
    ST -- "managed settings over SSH" --> DA
    ST -- "managed settings over SSH" --> DB
    GM -- "tasks, answers, approvals" --> DA
    DA -- "decisions to answer" --> GM
    GM -- "tasks, answers, approvals" --> DB
    DB -- "decisions to answer" --> GM
    DA -- "launch: prompt" --> AA1
    AA1 -- "result file" --> DA
    DA -- "launch: prompt" --> AA2
    AA2 -- "result file" --> DA
    DB -- "launch: prompt" --> AB1
    AB1 -- "result file" --> DB
```

Messages between a Director and its Artisans inform only; they never carry a result, a decision or an approval.
A machine may host several Directors, and each Director works only on its own machine.
Work moves between Directors only by manual handoff, never by automatic cross-Director scheduling.

### Inside a Director

The figure separates what is on `main` today from the boundaries that are designed but not built.

```mermaid
flowchart TB
    classDef built fill:#d8f3dc,stroke:#2d6a4f,color:#1b4332
    classDef planned fill:#f1f3f5,stroke:#868e96,color:#343a40,stroke-dasharray:5 4

    CFG["Config and workflow<br/>parsing and validation"]:::built
    STORE[("Store: tasks, attempts, decisions,<br/>rulings, ownership, receipts")]:::built
    MSG["Local message inbox"]:::built
    INSP["Inspection<br/>task timeline and files"]:::built
    DIR["Director orchestration<br/>command"]:::planned
    INTERP["Workflow interpreter<br/>Go library"]:::built
    RENDER["Prompt rendering<br/>and skills"]:::built
    WRAP["Artisan launcher<br/>containment, endpoint, stop ladder"]:::built
    ADAPT["Adapter contract<br/>and fake adapter"]:::built
    RT["Runtime adapter<br/>Claude Code first"]:::planned
    EXEC["Docker execution<br/>environment"]:::planned
    HERDR["Herdr tab<br/>presentation only"]:::built

    CFG --> DIR
    DIR --> INTERP
    INTERP --> STORE
    INTERP --> RENDER
    INSP --> STORE
    MSG --- STORE
    INTERP -. "not yet wired" .-> WRAP
    WRAP --> ADAPT
    ADAPT --> RT
    WRAP -.-> EXEC
    WRAP -.-> MSG
    ADAPT -. "shown in" .-> HERDR
```

Green boxes are on `main`; grey dashed boxes are designed but not built, and dotted arrows are connections not yet wired.

- The **Artisan launcher** owns each attempt's worker in a containment unit on the local machine; see [Artisan commands](docs/artisan.md#commands) for the implemented fake-adapter integration.
  Starting a real agent through a runtime adapter and delivering stored messages to it are not wired yet.
- A **runtime adapter** launches an agent's normal interactive session with a prompt file and working directory; an attempt completes only when the Artisan writes a valid result file, not when a process exits or a terminal closes.
  See [adapters](docs/adapters.md) for the implemented fake adapter and current runtime support.
- The **execution environment** is the local machine first; a Docker environment is a planned separate boundary and is not built.
- **Herdr**, a terminal multiplexer, shows each Director and Artisan in its own tab.
  A tab is a presentation binding only: closing it, restarting Herdr or rebooting never changes task state or loses work.
- **Messages** carry information only: a message never records a result, answers a decision or approves a merge.

### Workflows

Each task runs a JSON workflow made of built-in, versioned step kinds; a workflow cannot contain commands or code.
Each step names who fulfils it: an Artisan, the Guildmaster, the Director or Guildhall itself.
Loops are bounded, Guildmaster steps are durable pauses that spend no model tokens, and every repository's merge policy applies whatever the workflow says.
The shipped [default workflow](internal/workflow/default.json) is:

```mermaid
flowchart LR
    grill --> research --> plan --> accept{{"Guildmaster<br/>approval"}}
    accept -- "changes requested, max 3" --> plan
    accept --> build --> review["Artisan review"]
    review -- "changes requested, max 3" --> build
    review --> publish --> validate
    validate -- "failed, max 3" --> build
    validate --> deliver
```

A repository can have the Guildmaster review instead of an Artisan without changing any other repository; see [configuration and workflows](docs/configuration.md).
The [workflow interpreter](docs/interpreter.md) executes a supported subset as a Go library; no command runs a workflow yet.

## Build

Guildhall needs Go 1.27 or later.

```sh
go build -o guildhall ./cmd/guildhall
```

The release workflow publishes binaries for Linux, Windows and macOS, with a `SHA256SUMS` file, only for pushed version tags; manual runs produce downloadable workflow artifacts without publishing a release.
No release has been published yet.

## Commands

Each command is described in its own page; a new command adds its page and one line here.

- `guildhall version` prints the Guildhall version, platform and Go version.
- `guildhall doctor` prints the OS, architecture, Guildhall version and build-time Go version, then, for `git`, `ssh`, `claude` and `herdr`, the first non-blank version-output line and resolved path or why the version could not be read.
  A missing tool is reported, not treated as an error.
- `guildhall store doctor <path>` checks a store file read-only; see [the store](docs/store.md).
- `guildhall steward rollout` previews and applies rollouts of the private managed settings; see [Steward rollouts](docs/steward.md).
- `guildhall messages send|list|read|ack` handles local Director and Artisan messages kept in a store; see [messaging](docs/messaging.md).
- `guildhall workflow approve|approvals` records and lists workflow approvals; see [workflow-edit authority](docs/configuration.md#workflow-edit-authority).
- `guildhall render <request.json>` renders a prompt with embedded release skills first; see [prompt rendering](docs/rendering.md).
- `guildhall artisan run|stop|status|resume` runs, stops, classifies and resumes one owned Artisan attempt with the test-only fake adapter, and `guildhall fake-artisan` is that adapter; see [Artisan commands](docs/artisan.md#commands).
- `guildhall task new|show|list|timeline|files|finding rule|review` creates and inspects tasks and records the Director's rulings on review findings and full-review requests, without launching anything; see [tasks](docs/tasks.md).
- `guildhall task start` and `guildhall step done` advance a task through its workflow, launching each Artisan stage as an owned attempt; see [running a task](docs/running.md).
- `guildhall decide list|show|answer` lists, shows and answers decisions waiting for the Guildmaster; see [decisions](docs/decisions-cli.md).
- `guildhall help` lists the commands.

## Examples

### Check the build

```console
$ ./guildhall version
guildhall v0.0.0-20261020184157-8090cc664d0a linux/amd64 go1.27.2
$ ./guildhall doctor
os: linux
arch: amd64
guildhall: v0.0.0-20261020184157-8090cc664d0a
go: go1.27.2
...
```

The version and platform depend on your build, and the lines after `go:` report `git`, `ssh`, `claude` and `herdr` on your machine.

### Send and acknowledge a message

No command creates a store yet, so this example uses a copy of the store file the tests use, which holds task `t-1` in repository `app`.
Work on a copy, never on the committed file.

```sh
cp internal/store/testdata/schema-v1.store demo.store
cat > note.json <<'EOF'
{"schema": "guildhall.message/1", "id": "t-1-note-1", "recipient": "director", "repository": "app", "task": "t-1", "type": "note", "text": "Build finished; the plan is ready for review."}
EOF
./guildhall messages send --store demo.store director note.json
./guildhall messages list --store demo.store director
./guildhall messages ack --store demo.store director t-1-note-1
./guildhall store doctor demo.store
```

`send` prints the receipt only after the message is committed, and sending the same envelope again prints the same receipt and stores nothing new:

```json
{"mutation_id":"message:t-1-note-1","operation":"send-message","request_digest":"sha256:8c4f3149fed51b4795c3dedf5bb628671bd249f1c5923c77723be239346f628f","repository":"app","task_id":"t-1","sequence":8,"committed_at":"2026-10-20T19:06:04.839891945Z"}
```

`list` shows the unacknowledged message, and after `ack` the inbox is empty while the message stays readable by id:

```console
$ ./guildhall store doctor demo.store
store: demo.store
schema_version: 1 (supported: 1)
integrity: ok
receipts: 9
workflow approvals: 0
repository app: tasks 1, attempts 1, documents 1, pane bindings 1, decisions 1, messages 1 (0 unread)
```

### Preview a Steward rollout

A rollout needs a private config directory with a roster of enrolled machines, which this repository does not ship, and SSH access to those machines.
Use the [dry-run procedure](docs/steward.md#dry-run) to preview changes and save a local plan:

```sh
./guildhall steward rollout --config steward.toml --machine workstation --plan rollout-plan --dry-run
```

See [Steward rollouts](docs/steward.md) for the roster format, the printed plan, approval and `--apply`.

### A task, end to end (illustrative, not built)

This is the intended flow once Directors run tasks; the orchestration shown below is not implemented yet.

```mermaid
sequenceDiagram
    actor GM as Guildmaster
    participant D as Director
    participant S as Store
    participant A as Artisan
    GM->>D: Describe the change
    D->>A: Grill, research and plan
    A-->>D: Plan document
    D->>S: Record task, plan reference and decision
    D->>GM: Accept the plan?
    GM->>D: Yes
    D->>S: Record the exact answer
    D->>A: Build in an isolated copy
    A-->>D: Result file
    D->>A: Independent review against the accepted plan
    A-->>D: Review outcome for the exact head
    D->>GM: Ready to merge?
```

## Documentation

- `docs/requirements.md`: the product requirements.
- `docs/review.md`: the review standard every pull request is held to.
- `docs/decisions.md`: the index of engineering decisions, one file each under `docs/decisions/`.
- `docs/contracts.md`: how contracts, interfaces, tests and packages are organised, with one contract file per package under `docs/contracts/`.
- `docs/store.md`: the store's records, mutation receipts and locking.
- `docs/steward.md`: Steward rollouts.
- `docs/messaging.md`: the local Director and Artisan message inbox.
- `docs/adapters.md`: the Artisan adapter contract, result file and Herdr access.
- `docs/tasks.md`: creating Director tasks and the workflow and configuration they bind.
- `docs/running.md`: advancing a task with `task start` and `step done`.
- `docs/rendering.md`: Artisan prompts, skills, their manifest and Guildmaster questions.
- `docs/artisan.md`: the Artisan launcher's process identity, containment, control endpoint, stop ladder, heartbeat, reconcile classification and commands.
- `docs/glossary.md`: terms several packages use with different meanings.
- `CONTRIBUTING.md`: how to contribute.
