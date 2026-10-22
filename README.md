# guildhall

[![CI](https://github.com/ndrwl/guildhall/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/ndrwl/guildhall/actions/workflows/ci.yml?query=branch%3Amain)
[![Go version](https://img.shields.io/github/go-mod/go-version/ndrwl/guildhall)](go.mod)
[![License](https://img.shields.io/github/license/ndrwl/guildhall)](LICENSE)

**Run a team of AI coding agents on your own machines, and stay in charge without babysitting them.**

You tell a Director what you want in plain English.
It plans the work, hands each piece to an agent working in its own copy of your repository, has the result reviewed, and only interrupts you when it needs a decision: accept this plan, answer this question, merge this change.
Everything is written down as it happens, so closing a terminal or restarting your machine never loses work.

> **Status: early, not ready for real work.**
> Guildhall builds and runs on Linux, Windows and macOS, but no agent runtime or platform is supported for real work yet.
> The conversational Director described below is **planned, not built**.
> What you can try today is driving the same steps by hand with the `guildhall` commands; see [Try it today](#try-it-today) and [What works today](#what-works-today).

## Why Guildhall

Coding agents are good at doing one task while you watch.
Guildhall is for when you want several of them working while you don't:

- **One conversation, many agents.** You talk to one Director per machine, not to each agent. It keeps track of every task and agent for you.
- **You make the calls.** Plans, questions and merges wait for your answer, and waiting costs no model tokens. Agents never merge on their own say-so.
- **Your rules hold.** Each repository has its own merge policy and workflow, for example "a human reviews every change here", and a workflow cannot run arbitrary commands.
- **Nothing gets lost.** Task state lives in a file on disk, not in an agent's chat history or a terminal tab.
- **Your machines, your agents.** It runs on hardware you own and is built to work with agent tools such as Claude Code, so you can switch runtimes without losing work.

## What you could use it for

Once the Director is built, these are the kinds of jobs Guildhall is designed for:

- **Hand off a backlog.** "Add a health check endpoint, fix the flaky login test, and update the README." Each becomes its own task; independent tasks run in parallel.
- **Plan first, then build.** Each task is researched and planned, and you accept the plan before any code is written.
- **Reviewed changes, not raw diffs.** A second agent reviews each change against the plan you accepted before it comes to you to merge.
- **Several repositories, several machines.** Run a Director per machine, each covering one or more repositories, and keep shared settings in step with an optional Steward.

## How you will use it

You will talk to your Director, and never need to type a command to give work, answer a decision or approve a merge.
This is the standing rule, recorded in [plain English to the Director](docs/decisions/director-conversation-first.md).
The session below is illustrative: it shows the intended experience, which is not built yet.

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

Behind the scenes each task follows a workflow; the default one is research, plan, your approval, build, independent review, then delivery under the repository's merge policy.
You can change it per repository, for example to review changes yourself instead of an agent.
See [configuration and workflows](docs/configuration.md).

## Requirements

- Go 1.27 or later, to build Guildhall (no release has been published yet).
- `git`.
- To try the task commands: a git checkout of a repository to work on.
- Optional, depending on what you try: [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`claude`) for the minimal Claude adapter, `gh` for publishing and merging pull requests on GitHub, `ssh` for Steward rollouts, and Herdr, a terminal multiplexer, for showing agents in tabs.

`guildhall doctor` tells you which of these it can find.

## Install

```sh
git clone https://github.com/ndrwl/guildhall.git
cd guildhall
go build -o guildhall ./cmd/guildhall
./guildhall version
./guildhall doctor
```

```console
$ ./guildhall doctor
os: linux
arch: amd64
guildhall: v0.0.0-20261020184157-8090cc664d0a
go: go1.27.2
...
```

A missing tool is reported, not treated as an error.
Prebuilt binaries for Linux, Windows and macOS, with a `SHA256SUMS` file, will be published for version tags; side-by-side install and rollback is planned ([release upgrades](docs/releases.md)).

## Try it today

Until the Director exists, you can run the steps it will run yourself.
The quickest look needs nothing but the binary: copy the sample store, which holds one task, and inspect it.

```sh
cp internal/store/testdata/schema-v1.store demo.store
./guildhall task list --store demo.store --json
./guildhall task timeline --store demo.store --repo app t-1 --json
./guildhall decide list --store demo.store
./guildhall store doctor demo.store
```

Never point commands at the committed file itself; always use a copy.

To go further (create a task in your own repository, launch an agent for a stage, answer its decisions, send messages, preview a Steward rollout), follow the [walkthrough](docs/walkthrough.md).
It needs a small Director config naming your repository; see [configuration and workflows](docs/configuration.md).

## What works today

| | Status |
| --- | --- |
| Creating, inspecting and timing tasks; answering decisions; local messages | Built, driven by hand with commands |
| Advancing a task stage by stage and launching an agent for each stage | Built, with a test-only fake agent and a minimal Claude adapter whose live acceptance is pending |
| Publishing and merging a task's pull request through `gh` | Built |
| Steward rollouts of shared settings over SSH | Built, preview and apply |
| The conversational Director (`guildhall director start`) | Planned |
| Running stages that the Director itself performs, and background watching of running agents | Planned |
| Docker isolation for agents | Planned |
| Any runtime or platform supported for real work | Not yet: each needs to pass live acceptance first |

## The commands

The commands are the Director's tools; you can also call them yourself to script, inspect or drive work by hand.
`guildhall help` lists them, and each links to its own page.

- `guildhall version` and `guildhall doctor`: version, platform and which tools are installed.
- `guildhall task new|show|list|timeline|files|finding rule|review`: create and inspect tasks; see [tasks](docs/tasks.md).
- `guildhall task start` and `guildhall step done`: advance a task through its workflow; see [running a task](docs/running.md).
- `guildhall step retry`: start the next attempt of a step after a failed or stopped one; see [running a task](docs/running.md#step-retry).
- `guildhall decide list|show|answer`: decisions waiting for you; see [decisions](docs/decisions-cli.md).
- `guildhall artisan start|stop|status|resume`: control one agent attempt; `guildhall fake-artisan` and `guildhall claude-artisan` are the fake and Claude wrappers; see [Artisan commands](docs/artisan.md#commands) and [adapters](docs/adapters.md).
- `guildhall messages send|list|read|ack`: informational messages between the Director and its agents; see [messaging](docs/messaging.md).
- `guildhall workflow approve|approvals`: approve workflow changes; see [workflow-edit authority](docs/configuration.md#workflow-edit-authority).
- `guildhall steward rollout`: preview and apply shared settings to enrolled machines; see [Steward rollouts](docs/steward.md).
- `guildhall render <request.json>`: render an agent prompt with its skills; see [prompt rendering](docs/rendering.md).
- `guildhall store doctor <path>`: check a store file without changing it; see [the store](docs/store.md).
- `guildhall help`: list the commands.

## Learn more

- [Walkthrough](docs/walkthrough.md): drive a task by hand, step by step.
- [Configuration and workflows](docs/configuration.md): set up repositories, merge policies and workflows.
- [Architecture](docs/architecture.md): roles, what runs where, what is built, and what CI proves.
- [Glossary](docs/glossary.md): Guildmaster, Director, Artisan, Steward and other terms.
- [Requirements](docs/requirements.md): the full product specification.
- [Decisions](docs/decisions.md): why things are the way they are.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for local checks and how changes are reviewed, [docs/review.md](docs/review.md) for the review standard, and [docs/contracts.md](docs/contracts.md) for how contracts, interfaces, tests and packages are organised.
