# guildhall

[![CI](https://github.com/ndrwl/guildhall/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/ndrwl/guildhall/actions/workflows/ci.yml?query=branch%3Amain)
[![Go version](https://img.shields.io/github/go-mod/go-version/ndrwl/guildhall)](go.mod)
[![License](https://img.shields.io/github/license/ndrwl/guildhall)](LICENSE)

**Run a team of AI coding agents on your own machines, and stay in charge without babysitting them.**

You tell a Director what you want in plain English.
It plans the work, hands each piece to an agent working in its own copy of your repository, has the result reviewed, and only interrupts you when it needs a decision: accept this plan, answer this question, merge this change.
Everything is written down as it happens, so closing a terminal or restarting your machine never loses work.

> **Status: early, not ready for real work.**
> Guildhall builds and its tests pass in CI on Linux, Windows and macOS, but no agent runtime or platform is supported for real work yet.
> The conversational Director is built and exercised in CI against stand-ins, but has not yet run a real task; see [What works today](#what-works-today).
> What you can also try today is driving the same steps by hand with the `guildhall` commands; see [Try it today](#try-it-today).

## Why Guildhall

Coding agents are good at doing one task while you watch.
Guildhall is for when you want several of them working while you don't:

- **One conversation, many agents.** You talk to a Director, not to each agent.
  It keeps track of every task and agent for you.
- **You make the calls.** Plans and questions wait for your answer, and so do merges unless you set a repository to merge automatically.
  Waiting costs no model tokens.
  Agents never merge on their own say-so.
- **Your rules hold.** Each repository has its own merge requirements and workflow, for example "a human reviews every change here" or "merge by itself once review and CI pass", and a workflow cannot run arbitrary commands.
- **Nothing gets lost.** Task state lives in a file on disk, not in an agent's chat history or a terminal tab.
- **Your machines, your agents.** It runs on hardware you own and is built to work with agent tools such as Claude Code, so you can switch runtimes without losing work.

## What you could use it for

These are the kinds of jobs Guildhall is designed for:

- **Hand off a backlog.** "Add a health check endpoint, fix the flaky login test, and update the README."
  Each becomes its own task; independent tasks run in parallel.
- **Plan first, then build.** Each task is researched and planned, and you accept the plan before any code is written.
- **Reviewed changes, not raw diffs.** A second agent reviews each change against the plan you accepted before it comes to you to merge.
- **Several repositories, several machines.** Run a Director for one or more repositories on each machine, and keep shared settings in step with an optional Steward.

## How you will use it

You will talk to your Director, and never need to type a command to give work, answer a decision or approve a merge.
This is the standing rule, recorded in [plain English to the Director](docs/decisions/director-conversation-first.md).
The session below is illustrative, not a transcript of a real task: the conversational Director is built, but live acceptance of this experience is still pending.

> **You:** Add a health check endpoint to the app repository.
>
> **Director:** Created task t-2 in `app`. I'll research and plan it, then bring you the plan to accept.
>
> *(later)*
>
> **You:** What needs me?
>
> **Director:** One decision: the plan for t-2 is ready. It adds `/healthz` and a test. Accept it?
>
> **You:** Yes, but keep the migration behind a flag.
>
> **Director:** Recorded. Building t-2 now; I'll come back when it has been reviewed.
>
> *(later)*
>
> **Director:** t-2 passed review. Ready to merge?
>
> **You:** Merge it. And stop the build on t-3; I've changed my mind.

Behind the scenes each task follows a workflow; the default one is research, plan, your approval, build, independent review, then delivery under the repository's merge requirements.
You can change it per repository, for example to review changes yourself instead of an agent.
See [configuration and workflows](docs/configuration.md).

## Requirements

- Go 1.27 or later, to build Guildhall (no release has been published yet).
- `git`.
- To try the task commands: a git checkout of a repository to work on.
- Optional, depending on what you try: [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`claude`, subject to the [skill-policy compatibility requirements](docs/configuration.md#claude-skill-policies)) for the minimal Claude adapter, `gh` for publishing and merging pull requests on GitHub, `ssh` for Steward rollouts, and Herdr, a terminal multiplexer, for showing agents in tabs.

`guildhall doctor` tells you which of `git`, `ssh`, `claude` and `herdr` it can find; it does not check for Go or `gh`.

## Install

```sh
git clone https://github.com/ndrwl/guildhall.git
cd guildhall
go build -o guildhall ./cmd/guildhall
./guildhall version
./guildhall doctor
```

```console
$ ./guildhall version
guildhall v0.0.0-20261027210024-abd974855ac9 linux/amd64 go1.27.2
revision: abd974855ac9d1e8f0c0a7b6e2f5d4c3b2a19087 (2026-10-27T21:00:24Z)
latest merge: #158 (task d10)
behind main: 2 merges (#160, #159)
```

The latest merge and behind-main lines need a [copy](docs/running.md#copies) whose config lists this repository; `guildhall version --json` prints the same fields as JSON ([version](docs/contracts/cli.md#version)).

```console
$ ./guildhall doctor
os: linux
arch: amd64
guildhall: v0.0.0-20261020184157-8090cc664d0a
go: go1.27.2
...
```

A missing tool is reported, not treated as an error.
Prebuilt binaries for Linux, Windows and macOS, with a `SHA256SUMS` file, will be published for version tags; side-by-side installs, a per-copy release pin and the PATH launcher are built, and rollback is planned ([release upgrades](docs/releases.md)).

## Try it today

You can also run the steps the Director runs yourself.
A Director's copy is a directory holding its copy marker `.guildhall-copy`, its `config.toml`, its store `director.store` and its `state/` directory.
The quickest look needs nothing but the binary: create an empty store and the copy's marker with `store init --copy`, which refuses a store already there, and inspect it.

```sh
mkdir -p demo/state
./guildhall store init --copy demo
./guildhall task list --copy demo --json
./guildhall decide list --copy demo
cd demo && ../guildhall store doctor
```

Every command that takes a store accepts `--copy demo` in place of `--store demo/director.store`, `--config demo/config.toml` and `--state demo/state`, or, run anywhere inside the copy, finds it from the current directory by its marker; see [copies](docs/running.md#copies).

To go further (create a task in your own repository, launch an agent for a stage, answer its decisions, send messages, preview a Steward rollout), follow the [walkthrough](docs/walkthrough.md).
It needs a small Director config naming your repository; see [configuration and workflows](docs/configuration.md).

## What works today

| | Status |
| --- | --- |
| Creating, inspecting and timing tasks; answering decisions; local messages | Built, driven by hand with commands |
| Advancing a task stage by stage and launching an agent for each stage | Built, with a fake agent for tests and demos and a minimal Claude adapter whose live acceptance is pending |
| Publishing a task's pull request, waiting for its CI checks and merging it through `gh` | Built, exercised in CI against a stand-in `gh`; live GitHub behaviour is not claimed |
| Steward rollouts of shared settings over SSH | Built, preview and apply |
| The Steward session (`guildhall steward start`), owning the roster and rollouts | Built, supervised like a Director and exercised in CI against a stand-in runtime; live acceptance is pending; managed mode and lesson collection are planned |
| The conversational Director (`guildhall director start`) | Built in its own Herdr tab or the foreground, exercised in CI against a stand-in Claude Code and a stand-in terminal; pause, resume, stop, Director notes, reload and the automatic handoff triggers are built; see [tab acceptance status](docs/director.md#the-directors-tab) |
| Running stages that the Director itself performs, and background watching of running agents | Built: the Director finishes its own stages with `step finish`, and each attempt's launcher advances its task when the attempt ends and reports a stalled worker |
| Docker isolation for agents | Planned |
| Any runtime or platform supported for real work | Not yet: each needs to pass live acceptance first |

## The commands

The commands are the Director's tools; you can also call them yourself to script, inspect or drive work by hand.
`guildhall help` lists them, and each links to its own page.

- `guildhall version` and `guildhall doctor`: version, revision, latest merge, how far behind main, platform and which tools are installed.
- `guildhall task new|show|list|timeline|files|finding rule|review`: create and inspect tasks; see [tasks](docs/tasks.md).
- `guildhall task start` and `guildhall step done`: advance a task through its workflow; see [running a task](docs/running.md).
- `guildhall task cancel`: end an open task for good, with a reason, closing its pull request and voiding its decisions; see [tasks](docs/tasks.md#task-cancel).
- `guildhall task reconcile`: record a task done whose pull request was merged outside Guildhall; see [tasks](docs/tasks.md#task-reconcile).
- `guildhall step retry`: start the next attempt of a step after a failed or stopped one; see [running a task](docs/running.md#step-retry).
- `guildhall step park` and `guildhall step unpark`: hold a failed or stopped attempt on purpose, with a reason, so it leaves what needs the Director, and lift the hold; see [running a task](docs/running.md#step-park-and-step-unpark).
- `guildhall step send-back`: send a succeeded Artisan step back with Director feedback; see [send-back](docs/step-send-back.md).
- `guildhall decide list|show|answer`: decisions waiting for you; see [decisions](docs/decisions-cli.md).
- `guildhall director status --json`: everything in flight and what needs the Director or you, in one JSON document; see [the Director](docs/director.md#status).
- `guildhall director start` and `guildhall director ready`: run a Director session in its own Herdr tab or your terminal, and let a recovering session start new work; see [the Director](docs/director.md#start).
- `guildhall director pause`, `guildhall director resume` and `guildhall director stop`: hold new work while running attempts finish, lift the hold, and end the session and its attempts; see [the Director](docs/director.md#pause-and-resume).
- `guildhall director note add`, `guildhall director note edit`, `guildhall director note remove` and `guildhall director note list`: keep the Director's one-line notes, which every new session gets at startup; see [notes](docs/director.md#notes).
- `guildhall director maintenance record` and `guildhall director maintenance list`: record a note audit, and list every recorded run, hallmark runs recorded before hallmarks were recorded when their task finishes among them; see [maintenance](docs/director.md#maintenance).
- `guildhall director reload`, `guildhall director turn-started` and `guildhall director turn-ended`: restart the session at a quiet point or the end of the turn, and the runtime's turn-start and end-of-turn hooks; see [the Director](docs/director.md#reload).
- `guildhall director context-report`: the Director's status-line command, display-only with `--lanterns`; see [Lanterns](docs/lanterns.md) and [legacy context reporting](docs/director.md#automatic-handoff).
- `guildhall artisan start|stop|status|resume`: control one agent attempt; `guildhall fake-artisan` and `guildhall claude-artisan` are the fake and Claude wrappers; see [Artisan commands](docs/artisan.md#commands) and [adapters](docs/adapters.md).
- `guildhall messages send|list|read|ack`: informational messages between the Director and its agents; see [messaging](docs/messaging.md).
- `guildhall ci log <repo>/<task> <job|check>`: the full log of a failed CI job a task's validate results record, read-only and granted to every Claude Artisan; see [CI logs](docs/ci-logs.md).
- `guildhall clerk guild|decisions|task|inspect|queue`: read-only plain-text answers for the Guildmaster, behind the Clerk plugin's slash commands; see [Clerk](docs/clerk.md).
- `guildhall web [--copy <dir>]`: serve the copy's task pages on the tailnet, where the Guildmaster answers a task's decisions, as a Director session does; see [task pages](docs/web.md).
- `guildhall spend report --copy <dir> [--json]`: read-only recorded token and cost attribution by task, step and recorded role/profile; see [spend reporting](docs/spend.md).
- `guildhall quality judge|judged|agreement`: judge a delivered task's self-review in a separate session, take its judgement, and measure the judge against your audits; see [the quality rubric](docs/quality.md#running-it).
- `guildhall workflow approve|approvals`: approve workflow changes; see [workflow-edit authority](docs/configuration.md#workflow-edit-authority).
- `guildhall steward start`, `guildhall steward status` and `guildhall steward approval request`: run the Steward session, which owns the roster and rollouts, see its roster and each copy's last rollout, and open the decision through which you approve a rollout plan; see [the Steward session](docs/steward.md#the-steward-session).
- `guildhall steward rollout`: preview and apply shared settings to enrolled machines; see [Steward rollouts](docs/steward.md).
- `guildhall render <request.json>`: render an agent prompt with its skills; see [prompt rendering](docs/rendering.md).
- `guildhall store init <path>` and `guildhall store doctor <path>`: create an empty store file, or check one without changing it; see [the store](docs/store.md).
- `guildhall help`: list the commands.

## Learn more

- [Walkthrough](docs/walkthrough.md): drive a task by hand, step by step.
- [Configuration and workflows](docs/configuration.md): set up repositories, merge requirements and workflows.
- [Architecture](docs/architecture.md): roles, what runs where, what is built, and what CI proves.
- [Glossary](docs/glossary.md): Guildmaster, Director, Artisan, Steward and other terms.
- [Requirements](docs/requirements.md): the full product specification.
- [Decisions](docs/decisions.md): why things are the way they are.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for local checks and how changes are reviewed, [docs/review.md](docs/review.md) for the review standard, and [docs/contracts.md](docs/contracts.md) for how contracts, interfaces, tests and packages are organised.
