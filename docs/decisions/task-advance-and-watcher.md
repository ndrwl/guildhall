---
status: accepted
date: 2026-10-22
topic: engine
formerly: D52, 0052-task-start-and-step-done.md, D64, 0064-launcher-advances-its-task.md, D76, 0076-watcher-advance-failed.md
---

# Commands and each attempt's launcher advance a task, one advance at a time under the task's lock

## Context

The interpreter had to be joined to the owned launcher so that `task start` and `step done` run real Artisan attempts.
The Director should then receive results and failures without typing either command (R1.29), and a failed advance must not leave a finished attempt unnoticed (sustainability review 11, F35).

## Decision

- **One advance per command.** `guildhall task start` and `guildhall step done` each run one interpreter advance; `launch.Process` runs every Artisan attempt as its own detached owned launcher over the directory the interpreter prepared, and `artisan.AttemptKey` alone owns the attempt layout (review 7 F19).
- **The store is opened per operation.**
  An advance opens the store for each record operation, as the launcher does, because `Process.Launch` returns only once the launcher owns the attempt and that needs the store's single-writer lock.
- **The task's own lock serialises advances.**
  Two advances of one task are serialised by `attempt.lock` in `<state>/<repository>/<task>`.
- **First outcome stands.**
  The launcher and result intake both record an attempt's outcome, because either may happen first ([replay and recovery](../interpreter.md#replay-and-recovery)).
- **`task start --decision <id>`** feeds `decision-answered`, because answering a decision never advances a task.
- **The launcher is the watcher.**
  After recording the outcome and releasing ownership, a launcher started from a copy (`--copy <dir> --advance`) runs `step done` for a finished attempt with a valid result and `task start` otherwise, as a child; no process, daemon or poll is added, and the advance is the commands' own engine assembly, not a copy inside the launcher ([the watcher](../running.md#the-watcher)).
- **A failed watcher advance is retried once, then told.**
  It runs once more, absorbing a task lock held for under about a minute; if that fails too, the launcher sends the Director `advance-failed`, one id per attempt, through its outbox with no task lock, and `director status` derives an `advance` item first in `needs_me` from the records.
- Every command-line advance writes what the task now waits for to `next.json` beside the task lock while holding it.

## Alternatives rejected

- Holding the store for the whole advance: it deadlocks with the launcher taking ownership.
- A separate watcher process, daemon or poll: the launcher already outlives the command and knows when its attempt ends.
- Waiting for the task lock without bound: a held lock would make a launcher that never exits and an attempt `artisan stop` cannot settle.
- Deriving the `advance` item from the message: the message is read once, but the state lasts until an advance takes the attempt.

## Consequences

A watcher advance's result never changes the attempt's outcome or the launcher's exit status; it is logged in `launcher.log` and as an `advance` event.
A Herdr-tab attempt and one started by hand without a copy take no `--advance`; `artisan resume` settles but does not advance.
`director status` reads where each task last stood from `next.json` without the launcher keeping state.

## History

- 2026-10-21, D52 (0052-task-start-and-step-done.md): joined `task start` and `step done` to the owned launcher, with the per-operation store open, the task's `attempt.lock`, first-outcome-stands and `task start --decision`; the caller ran `step done` once a result existed until a watcher landed.
  Its slice scaffolding (the `fake` default adapter, the Director-local `fake-straight` acceptance workflow, the next slice's plan) is dropped; runtime selection is now under [profiles](../configuration.md#profiles).
- 2026-10-22, D64 (0064-launcher-advances-its-task.md): made the launcher the task's watcher, running the advance only after the outcome is published and ownership released, so it never acts before a cancel's marker decision; a failed advance was only logged, since both commands are idempotent and the Director's next `task start` repeats it.
  Added `next.json`.
  Rejected then: a new process, daemon or poll, and a second engine assembly inside the launcher.
- 2026-10-22, D76 (0076-watcher-advance-failed.md): amended the watcher's failure rule, leaving the rest standing, after review found a watcher advance that lost the task lock (30 seconds, exit 1) left a stale `next.json`, no message and no wake; `director stop` ([pause and stop](director-pause-stop.md)) and the attempt caps ([guard rails](guard-rails.md)) made that collision routine.
  Added the retry, `advance-failed` and the `advance` status item.
  Rejected then: waiting for the lock without bound.
