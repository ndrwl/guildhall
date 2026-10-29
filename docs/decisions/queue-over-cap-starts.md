# Queue over-cap starts

A start over `max_running_attempts` used to be refused: it recorded nothing, left `next.json` unchanged and sent a `budget-refused` message, and nothing started it later ([guard rails](0074-guard-rails.md)).
The Director then repeated `task start`, `step retry` and `step send-back` by hand whenever a slot freed, about fifteen times in one night, and Director stages (grills) took running slots although they run in the Director's own session.
This supersedes the guard rails' bullets on the refusal and on "nothing relaunches a refused task by itself" for the running cap; the daily cap is unchanged.

## The rule

When the running cap is full, a start is recorded as queued instead of refused.
When a slot frees, the queued start with the highest current priority starts next; ties go to the oldest queue time, then to the task id.
Nothing is preempted: p0 only goes to the front of the queue.
Director stages never count against the running cap and never queue.

## Store

- A per-repository `start-queue` bucket holds at most one entry per task and step: the attempt number, the queue time by the store's clock, and the request that asked for the start (`start`, `decision`, `retry` with the retried attempt, `send-back` or `engine`).
- `store.OrderStartQueue` is the one ordering rule, shared by the store and the `interptest` double, as `OrderMergeQueue` is.
  Priority is read live, never snapshotted, so `task priority` reorders the queue at once.
  Entries of closed tasks, and of attempts since recorded, are left out when the queue is read, so they never hold the front.
- `StartCappedAttempt` with `AttemptCaps.Queue` makes the admission decision in its single write.
  It admits a start only while the count is under the running cap and the queue is empty or this start is its first entry, so a task whose own attempt just finished cannot take the freed slot ahead of a waiting p0.
  Admission records the attempt and removes the start's entry in the same write, so there is never both an attempt and an entry, nor neither.
- Any other start writes or keeps its entry, under its own mutation id `<repo>.<task>.queue-start.<step>-<n>`, and returns a `*store.StartQueued` with its place.
  The start's own mutation id stays unrecorded until admission, so its later replay is evaluated afresh; queuing an existing entry again keeps its queue time and request.
- The Director hold is still checked first: a held start is refused with `ErrDirectorHeld` and never queued.
  Replaying an admitted start's mutation id returns its receipt whatever the caps and the queue are now.
- The daily cap still refuses with a `*CapRefusal` and a `budget-refused` message: queuing until 00:00 UTC would need another drain trigger and hold work silently overnight.
- Quality-judge starts pass no `Queue` and keep the raw refusal.
- `StartDirectorAttempt` starts a Director stage hold-checked, uncapped and never queued, and flags the attempt `director`; `CountUsage` leaves flagged attempts out of `Running`.
  Attempts recorded before the flag count until they finish, which does no harm.
  Director stages still count toward the day's starts.

## Engine

A queued start is not an error: the advance returns `WaitQueued` with the attempt and its place, launches nothing and sends no message.
The binding is still recorded before the capped start, so a queued start keeps its inputs, a retry's first bound reason among them, and the retry's reason message is still sent only at launch.
A failed or stopped latest attempt with a queue entry for the next attempt counts as an owed retry, as an `attempt-retried` event does, so a plain `task start`, such as the start-queue watcher's, starts it; send-backs and `--decision` starts already resumed on a plain advance.

## Draining

The session supervisor's fourth watcher, `start-queue`, is built like the checks, dependencies and merge-queue watchers and on their interval, `max(checks_refresh, 30s)`.
Each round reads the queue in order and advances each queued task as `task start` does, from the front, stopping at the first that stays queued.
An entry whose advance neither starts nor queues it, such as one the dependency gate holds or one that fails, leaves the queue and is logged, so it does not hold the front; the task's `next.json` reports that advance as any other.
A freed slot can therefore sit idle for up to about 30 s; a launcher-to-supervisor signal would close that gap, at the cost of a new channel, and was not needed yet.
Like the other watchers it advances nothing while the session is paused, stopping or sweeping, and it is drained before stop.

`director status --json` lists the queue as `session.start_queue`, outside `needs_me`, since a queued start is not something the Director acts on.
