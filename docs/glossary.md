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
