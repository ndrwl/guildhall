---
status: accepted
date: 2026-10-22
topic: engine
formerly: D62, 0062-failed-attempt-retry.md
---

# A failed or stopped attempt has a next attempt, started by step retry

## Context

After a failed or stopped attempt the engine waits for the Director ([task start and step done](task-advance-and-watcher.md)), but no event or command expressed the Director's decision to try again.
The engine starts an attempt only when a step has none, or after a succeeded one whose inputs moved, so a task whose launcher was killed waited for good (sustainability review 9, F27).
R1.34 says work is not capped by an attempt budget, so the next attempt must be reachable.

## Decision

A fourth event, `attempt-retried` with a step and attempt number, carries the decision, and `guildhall step retry --task --step --attempt [--reason]` feeds it ([events](../interpreter.md#events), [running a task](../running.md#step-retry)).
It is valid only when the named attempt is the step's latest and finished `failed` or `stopped`; the engine then starts the next attempt through the same path as a first attempt.
Naming the attempt makes the decision about one outcome: a retry of attempt 1 arriving after attempt 2 launched is a replay that records and launches nothing beyond a reason an interrupted retry had not yet sent, and one aimed at an older attempt is refused, so a stale or repeated command cannot start two attempts.
Refusals use the engine's `ErrEvent`, as every event that does not match the records does; the engine has no `ErrInvalid` of its own.

The optional reason is sent as Director message `<repo>.<task>.attempt-retried.<step>-<n>`, under an id derived from the retried attempt, only after the next attempt has launched and `launched.json` is written.
A retry whose start or launch fails sends nothing, so the Director can repeat it with another reason; a crash between the launch and the message is recovered by replaying the retry, and a replay with a different reason is ignored.

A failed or stopped attempt no longer counts as having served loop feedback, so the attempt that retries a looped build is given the reviewer's feedback again.

## Alternatives rejected

- Overloading `task start` with a flag: `task start` feeds `task-created`, which means the task was created, and the reason would go unrecorded.
- Naming only the step: a stale or repeated command could start two attempts.
- Sending the reason once the attempt was recorded: a failed launch would store the first reason and silently drop a later different one as a replay.
- A required reason: a retry after a killed launcher needs no explanation, and a required one would be filled with noise.
## Consequences

When a failure came after the next attempt was recorded, a repeated retry finds that attempt unlaunched, launches it and then sends its own reason.
A retry is distinct from `step send-back`, which reruns a succeeded step with Director feedback.
