# Glossary

Some terms name different things in different packages.
This page says which is meant where.

## Launcher

- `artisan.Launcher` is the per-attempt process that owns one Artisan worker: it runs the worker in its containment unit, answers the control endpoint, writes the heartbeat and decides the attempt with the [stop ladder](artisan.md#stop-ladder).
- `interp.Launcher` is the interpreter's seam for starting and stopping attempts ([mock agent](mock-agent.md)); its real implementation, `launch.Process`, runs `guildhall artisan start`, whose process is an `artisan.Launcher`.

## Outcome

How an attempt ended is spelled three ways, and `launch.attemptOutcome` is the one translation between them:

| Package | Type | Values | Meaning |
|---|---|---|---|
| `adapter` | `Status` | `succeeded`, `failed` | What the Artisan reported in its result file |
| `artisan` | `OutcomeState` | `finished`, `cancelled`, `failed` | What the launcher decided under its [precedence rules](artisan.md#precedence) |
| `store` | `AttemptOutcome` | `succeeded`, `failed`, `stopped` | What the Director records for the stage attempt |

| Launcher outcome | Result status | Store outcome |
|---|---|---|
| `cancelled` | any | `stopped` |
| `failed` | none | `failed` |
| `finished` | `succeeded` | `succeeded` |
| `finished` | `failed` | `failed` |

`failed` in `artisan` includes a worker ending without a valid result and launcher errors such as worker startup failure; in `adapter` and `store` it means the work failed.
A finished outcome without a readable result is refused, not guessed.
`workflow.ReviewOutcome` is a review's verdict and `interptest`'s `Outcome` a scripted attempt's; neither is an attempt's end.

## Identity

- `adapter.Identity` is a task and attempt id pair, such as `t-1` and `build-1`, that a result file must name.
- `artisan.Identity` is a process: its PID and a creation token that differs for a later process reusing the PID.
- `artisan.AttemptKey` names an owned attempt as repository, task, step and attempt number; its adapter identity is the task and `<step>-<number>`.

## Forge

- A forge is the service that hosts a repository's pull requests, such as GitHub.
- `interp.Forge` is the interpreter's seam for delivery: it publishes a task branch as a pull request and merges it ([forge contract](contracts/forge.md)).
- `forge.GitHub` is its only implementation, over the `gh` program; any other remote is refused ([D60](decisions/0060-delivery-stage.md)).

## Copy

- A Director copy is one directory holding `config.toml`, `director.store` and `state/`, selected with `--copy <dir>` or `GUILDHALL_COPY` ([copies](running.md#copies)).
- `store init --copy <dir>` creates its store; a copy is not a release install, which [release upgrades](releases.md) design.

## Session

- A Director session is one run of the Director's runtime under its supervisor, `director start`, recorded in the store and numbered from 1 ([Director sessions](store.md#director-sessions), [D67](decisions/0067-director-session-record.md)).
- A Herdr session is the terminal multiplexer's own server session; a Director session records the Herdr session its pane is in.
- `render.Session` renders a Director session's startup files into `<copy>/director/session-<n>/` ([rendering](rendering.md#director-sessions)).

## Wake

- A wake is the one decision a sender records for each message to the Director: `woke`, when it typed a line of unread counts into the Director's pane, or `queued` with a reason ([waking the Director](messaging.md#waking-the-director), [D69](decisions/0069-director-wake.md)).
- `store.Wake` is that record; `director status` shows the newest as `last_wake`, `messages list` shows each message's and `store doctor` counts them.

## Worktree

- A task's working directory, `<state>/<repo>/<task>/work`, is a git worktree of the repository's `path` on branch `guildhall/<task>` ([the working directory](interpreter.md#the-working-directory), [D68](decisions/0068-worktree-custody.md)).
- It is neither the Director copy nor the repository's own checkout at `path`.
