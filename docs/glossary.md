# Glossary

Some terms name different things in different packages.
This page says which is meant where.

## Launcher

- `artisan.Launcher` is the per-attempt process that owns one Artisan worker: it runs the worker in its containment unit, answers the control endpoint, writes the heartbeat and decides the attempt with the [stop ladder](artisan.md#stop-ladder).
- `interp.Launcher` is the interpreter's seam for starting and stopping attempts ([mock agent](mock-agent.md)); its real implementation, `launch.Process`, runs `guildhall artisan start`, whose process is an `artisan.Launcher`.

## Kind

- A **task kind** is what a task is created as, `feature` or `bug`, chosen with `task new --kind`; it chooses the task's workflow ([task kinds](tasks.md#task-kinds)).
  In Go it is `taskKind`, `config.TaskKindFeature` and `config.TaskKindBug`, never a bare `Kind`.
- A **step kind** is a built-in, versioned step type a workflow names, such as `research` or `review`: `workflow.Kind` in the [registry](configuration.md#built-in-step-kinds).
  A task kind's workflow is made of step kinds.

## Profile

- A **role profile** is the Artisan profile a step runs under because of its step kind when it names no `profile`: `researcher`, `planner`, `builder`, `reviewer`, `rebaser` for the merge queue's rebaser, and `designer` for a design round's designer ([profiles](configuration.md#profiles)).
  It is resolved at use, `workflow.EffectiveProfile`, and is outside the workflow's digest; Guildhall ships all six, and a copy's `[profile.<name>]` replaces one.
  Avoid "step profile" for it and "dogfood profile".
- A **repository grant**, `[repository.profiles.<name>]`, is what one repository adds to a profile's effective grant, such as a Go repository's `go test` for its builder ([Artisan permissions](adapters.md#artisan-permissions)).
  It is also called a **repository override**; the docs say repository grant.

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

## CI logs

- A **failed job log** is the CI log of one failed check's GitHub Actions job, as Guildhall fetches it from the forge with its own `gh`, at most `ci_log_limit` bytes of its tail ([validate](interpreter.md#validate)).
- A **log excerpt** is the bounded tail of the failing step of a failed job log, inlined in a failed validate result's `logs` ([validate](interpreter.md#validate)).
- The **CI log cache** is `<state>/<repo>/<task>/ci-logs/`, the failed job logs validate and [`ci log`](ci-logs.md) fetched; it is no store document and may be deleted at any time.
- Avoid "build log" for any of these: a build is the Artisan's build step, not CI.

## Copy

- A Director copy is one directory holding its copy marker, `config.toml`, `director.store` and `state/`, selected with `--copy <dir>` or by copy discovery ([copies](running.md#copies)).
- `store init --copy <dir>` creates its store and its copy marker; a copy is not a release install, which [release upgrades](releases.md) design.
- The **copy marker** is the `.guildhall-copy` file at a copy's root that copy discovery looks for ([the copy marker](running.md#the-copy-marker)).
- **Copy discovery** finds the copy by searching upward from the current directory for the copy marker; a start under the copy's `state/` finds none ([copies](running.md#copies)).

## Cancel

- To **cancel** a task is to end it for good without delivering it, `task cancel --reason <text>`, at the Guildmaster's request ([task cancel](tasks.md#task-cancel)); a task is not deleted, removed or closed.
- A **cancelled** task is in the terminal state `cancelled`, with its reason and time; it is never advanced, judged or audited again, and is not un-cancelled.
- Cancelling a task **closes** its open pull request, keeping the branch, and **voids** its open decisions: a **void** decision waits for no one and cannot be answered.

## Dependency

- A task's dependencies are the tasks named by `task new --depends-on`, fixed at creation ([dependencies](tasks.md#dependencies)).
- The **dependency gate** stands before a task's first step not run by the Director, such as research after the grill: it passes once every dependency is `done` (merged), or the Director overrides ([the dependency gate](interpreter.md#the-dependency-gate)).
- A task **held by a dependency** is one the gate holds: its last advance recorded nothing and waits `{"wait":"dependencies"}`, the Director has a `dependency-held` message and `director status` a `dependencies` item.
- A **dependency override** is the Director's recorded decision, with a reason, to start a held task anyway, `task start --ignore-deps --reason <text>`; it lasts for the rest of the task's life ([`task start`](running.md#task-start)).
- None of these is the `dependency-notice`, a designed message type between Directors ([Director messaging](messaging.md#message-types)).

## Design round

- A **design round** is one design the Director asks for with `task design`, on a feature task before its first build: a designer Artisan writes the design and the Guildmaster decides on it in decision `design-<n>` ([design rounds](tasks.md#design-rounds)).
  It is a Director tool, not a workflow step; say design round, not "design loop" or "design step".
- **Rounds used** is how many design rounds a task has had, one per accepted request whatever its outcome, printed as `design_rounds`; a retry of a failed designer stays within its round.
  There is no cap on them; say rounds used, not "cap" or "limit".
- The **designer** is the round's Artisan, the synthetic step `designer`, run under the copy's `designer` profile, shipped at effort `high`.

## Merge queue

- A repository's **merge queue** holds its tasks whose merge is authorized, the merge decision answered `merge` or `<deliver>-<k>-authorized` recorded, in order of priority, then ready time, then task id ([the merge queue](interpreter.md#the-merge-queue)).
  It is Guildhall's own, kept in the store; it is not GitHub's merge queue.
- The **front** is the one task of a queue that may be rebased and merged, claimed in one store mutation; it keeps the front until it merges, is held, is sent back or is cancelled. A **stay** is one time at the front.
- A task's **ready time** is the commit time of the receipt that first authorized its merge; it survives a major send-back and a hold, and goes only with a merge or a cancellation.
- A **merge hold** is `task hold-merge`'s recorded hold: the task stays authorized and keeps its ready time, but the queue skips it ([holding a merge](tasks.md#holding-a-merge)).
  The `hold` answer on a merge decision is another thing: it refuses the merge it was asked for, and the task waits for the Director.
- **Priority** is a task's level, `p0 urgent` to `p5 someday`, `p3` by default ([priority](tasks.md#priority)).

## Rebase

- A **rebase** here is Guildhall replaying a task's whole change as one commit onto the base tip mergeability measured, with `git merge-tree`, never `git rebase` ([tiered rebase](interpreter.md#tiered-rebase)).
  A send-back that rebases the task branch, or the [rebase-aware review](interpreter.md#rebase-aware-review) of such a build head, is the Artisan's rebase, not this one.
- The **rebase kinds** are `no_conflicts` (no conflicts and equal patch-ids), `minor_conflicts` (few conflicts with markers, within `minor_conflict_files` and `minor_conflict_lines`, or unequal patch-ids) and `major_conflicts` (anything else, which sends the build back); as `reapprove_after_rebase`, `never` follows them as a threshold no rebase reaches ([rebase settings](configuration.md#rebase-settings)).
- The **rebaser** is the Artisan that resolves a `minor_conflicts` rebase, run as the synthetic step `<step>-rebase` under the copy's `rebaser` profile.
- The **narrowed review** is the review of a rebaser's resolution, `<step>-rebase-review`, scope `resolution`: it reads only `diff marked..resolved`, the resolved hunks.
- A **carried-over approval** is `<deliver>-<k>-authorized` with schema `guildhall.merge-authorization/2`: an earlier merge approval standing for a rebased head below `reapprove_after_rebase`.

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
