---
status: accepted
date: 2026-10-29
topic: delivery
tasks: d10, d25
formerly: D74, 0074-guard-rails.md, unlimited-daily-attempts.md, queue-over-cap-starts.md
---

# Guard rails are enforced by the binary: a start over the running cap is queued, and the daily cap may be turned off

## Context

A Director's guard rails are limits the binary enforces whatever the Director asks ([guard rails](../director.md#guard-rails)): the repositories it may act on, each repository's merge rule, and caps on the Artisan attempts it starts.
Refusing every start over a cap left the Director repeating `task start`, `step retry` and `step send-back` by hand whenever a slot freed, about fifteen times in one night, while grills took running slots although they run in the Director's own session.
The Guildmaster also wanted the daily cap off, and was setting `max_attempts_per_day = "10000"` as a stand-in.

## Decision

- `config.GuardRails` returns all three rails; the caps are settings read through `Config.Setting`, so a Steward's managed value is read-only to the Director and a standalone copy sets them under `[local]`.
- **Repositories.**
  Every command that acts on a repository finds it through `config.ListedRepository`, which refuses one the config does not list (`ErrRepositoryNotListed`) before reading a result file, locking the task or touching the store; `messages send` checks the envelope's repository when it has a config, and one over `--store` alone is unchanged, because the store already refuses a message about a task it does not hold.
- **Merge rule.**
  Each repository's effective [merge requirements](delivery-stage.md), which deliver enforces and status reports as `merge_requires`.
- **Caps.** `max_running_attempts` (default 4, at least 1) and `max_attempts_per_day` (default 40; `"0"` is no daily cap) are per Director, across every repository, and meter attempts, not tokens or cost; each attempt's [spend](attempt-spend.md) is shown beside them.
- **One write.**
  The count and the start are one store mutation, `store.StartCappedAttempt`, under the single-writer lock.
  Running is every attempt started and not finished, Director stages left out; the day is the UTC day of the store's clock; the caps are not part of the start's request, so a replayed start is launched, never refused.
- **Over the running cap, a start is queued** in a per-repository start queue, one entry per task and step, ordered by live task priority, then queue time, then task id (`store.OrderStartQueue`), and admitted only while under the cap and first in the queue, so a task whose own attempt just finished cannot jump a waiting p0; nothing is preempted ([the start queue](../director.md#the-start-queue)).
  The advance returns `WaitQueued` and sends no message, and the session supervisor's start-queue watcher drains the queue every `max(checks_refresh, 30s)`.
- **Over the daily cap, a start is refused**: nothing is recorded or launched, the advance exits 1, and the engine sends one `budget-refused` message per task and UTC day; nothing relaunches it by itself.
- Director stages never count against the running cap and never queue, but count toward the day's starts.

## Alternatives rejected

- A cap check before the start: two advances in different processes could both take the last slot.
- Counting unreleased ownership records, as planned: an attempt is recorded before its launcher takes ownership, so launches in flight would be missed.
- Queuing over the daily cap until 00:00 UTC: it would need another drain trigger and hold work silently overnight.
- Relaunching a refused task automatically: it needs a scheduler the binary does not have, so the message names the command.
- A launcher-to-supervisor signal when a slot frees: a new channel, not needed yet, at the cost of a slot idle for up to about 30 s.
- A running cap of 0: it would refuse every attempt, and only the daily cap was asked to be optional.

## Consequences

`director status` shows the rails and counts under `session.guard_rails`, read in the same transaction as the rest of the feed, and the queue as `session.start_queue`, outside `needs_me`, since a queued start is not something the Director acts on.
A queued entry whose advance neither starts nor queues it leaves the queue and is logged, so it never holds the front.
Quality-judge starts are not queued and keep the raw refusal, and attempts recorded before the Director-stage flag count until they finish.
A token or cost budget is a follow-up.

## History

- 2026-10-22, D74 (0074-guard-rails.md): set the three rails, the one-write capped start and the `budget-refused` message, refusing a start over either cap; the merge rule was then the repository's merge policy, only reported here, and became the merge requirements on 2026-10-28 ([delivery](delivery-stage.md)).
  The Guildmaster's request for each attempt's token use and cost alongside the caps was met by [attempt spend](attempt-spend.md).
- 2026-10-27, unlimited-daily-attempts.md (d10): made `max_attempts_per_day = "0"` no daily cap, managed or local, where anything below 1 had been refused.
  The store and engine already read a zero `MaxPerDay` as no cap, so only config validation changed: `Parse` and `GuardRails` share one rule that accepts a whole number written plainly and refuses `"-1"`, `"00"`, `"+0"` and `" 0"`.
  With no daily cap, `attempts_today` and `spend_today` are still counted ([red run](../evidence/unlimited-daily-attempts-2026-10-27-red.txt)).
- 2026-10-29, queue-over-cap-starts.md (d25, split from d23's Q6): replaced D74's refusal over the running cap, and its "nothing relaunches a refused task by itself", with the start queue and its watcher, and took Director stages out of the running count through `StartDirectorAttempt` and a `director` flag; the daily cap kept its refusal for the reasons above.
  A queued start keeps its recorded binding, and a failed or stopped attempt with a queued next attempt counts as an owed retry, so a plain `task start` starts it.
