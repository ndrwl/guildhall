---
status: accepted
date: 2026-10-23
topic: director
formerly: D67, 0067-director-session-record.md, D71, 0071-director-start.md, director-startup-admission.md
---

# The Director runs as a supervised session, recorded in the store, that recovers before it starts new work

## Context

The Director is a long-running conversation, so Guildhall needs a durable answer to "which Director session is current and what state is it in", for the status feed, pause and stop, wakes, and a session started after a crash.
Before `director start` the Director was a bare `claude` with no record, and the [role grammar](cli-role-grammar.md) asks for it to run as a Guildhall session.

## Decision

- `guildhall director start [runtime]` is the session supervisor: it runs a preflight, records the session, renders what the runtime reads, runs the runtime as its child in the current terminal and records the end ([start](../director.md#start)).
  Foreground in the current terminal is the default, because that is what a user typing the command most likely wants; the [Herdr tab](director-tab.md) is opt-in with `--tab`.
- Sessions and wakes live in a top-level `director` bucket, created on first use, so the store schema stays 1; they are not per repository, because one Director spans every repository in its store ([Director sessions](../store.md#director-sessions)).
- A session is numbered from 1, starts `recovering` and moves only forward, through `running` or `paused`, `stopping` and `stopped`, except that `running` and `paused` move to each other.
  Starting session n+1 records any session not yet stopped as `stopped` in the same transaction, so exactly one session is ever current.
- A state move names the receipt sequence of the state it leaves (`director-state:<n>:<state>:<from>`, stored as `StateSequence`), so a retried move replays and a move decided on a stale read, such as a pause racing a stop, is refused rather than applied over the newer state.
- A wake is one record per message to the Director, keyed `wake:<message id>`, so a replayed send finds its decision and types nothing new; it records the sender type, so a per-sender wake budget needs no new shape.
  The store checks only the record's form and that the message is addressed to the Director; deciding to wake is the wake's job.
- A `recovering` session starts no new work until the Director runs `guildhall director ready`, enforced only at `task new` and at `task start` of a task with no attempt, so recovery itself (`step done`, `step retry`, `decide answer`, every read, advancing a task already under way) is never blocked.
- `startup.md` lists `director status`'s `needs_me`, built by the same function, so what a new session is told and what an outside monitor sees cannot drift.
- `runtimes.LookupSession` is the one owner of the session runtimes, as `Lookup` is of attempt runtimes; each names the program it needs on `PATH`, its skills directory and its own session files.
  Claude Code runs in a [stable workspace](director-stable-claude-workspace.md), and its options always end with `--` before the first message, because `--add-dir` is variadic and would otherwise take the message as a directory.
- The preflight requires `guildhall` on `PATH`, after the runtime check and before the store is opened, because hooks, the status line and the Director's commands use that bare name; it checks availability through `App.LookPath`, not that the binary is the supervisor's release.
- The fake Director and the Claude stub call no model and play the same script (`fake.RunScript`), so the session contract and the CI safety checks run the same steps on both.

## Alternatives rejected

- `paused` as a flag on `running`: pause stops new work only while running attempts finish, so a paused session is still alive and must still be stoppable.
- A stored reason when a start records an older session `stopped`: the newer session's start time already says why.
- Blocking every command while `recovering`: that would block the recovery the state exists for.

## Consequences

Until managed mode lands, a copy with `managed.toml` or inline `[managed]` settings is refused, naming `guildhall director enrol`.
`director status` does not yet probe the supervisor's identity for liveness.
The doubles prove only the command shape and settings the adapter writes; live Claude acceptance is a lab run, not CI.

## History

- 2026-10-22, D67 (0067-director-session-record.md): designed the session and wake records ahead of the supervisor, with the state machine, the compare-and-set on `StateSequence` (a field beyond the planned shape) and the replayable wake; status reported the newest session, and the `recovering` item in `needs_me` waited for the supervisor.
- 2026-10-22, D71 (0071-director-start.md): built the supervisor in the foreground, with `recovering` held until `director ready`, `startup.md` built from the status feed and one session shape per runtime; it rendered every release skill, first root winning, because the Director's own skills were not written yet (they since are, [Director skills](director-skills.md)), and its settings allowed `guildhall` and read-only `git`, denied `git --output` and edits to `config.toml` and `managed.toml`.
  The pre-release `--foreground` flag was removed with no alias once the [Herdr tab](director-tab.md) made `--tab` the opt-in; session model and effort came to follow [runtime settings](../adapters.md#runtime-settings); the reload loop, handoff notes and context reporter landed with [handoff and reload](director-reload-and-notes.md).
- 2026-10-23, director-startup-admission.md: terminated Claude's session options with `--` with or without a model, after an unseparated first message was taken by `--add-dir`, and required `guildhall` on `PATH` in the preflight, after a start by absolute path left hooks unable to run; the interactive stub refuses the old shape, the print-mode Artisan arguments were unchanged, and no new process abstraction was added.
  Tests failed first for both faults and make no model calls; the empty-cgroup recovery after a superseded session was left as a follow-up.
