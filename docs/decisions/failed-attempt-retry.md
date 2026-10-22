# A failed attempt has a next attempt

After a failed or stopped attempt the engine waits for the Director ([D52](0052-task-start-and-step-done.md)), but no event or command expressed the Director's decision to try again: the engine starts an attempt only when a step has none, or after a succeeded one whose inputs moved, so a task whose launcher was killed waited for good (sustainability review 9, F27).
R1.34 says work is not capped by an attempt budget, so the next attempt must be reachable.

A fourth event, `attempt-retried` with a step and attempt number, carries the decision, and `guildhall step retry --task --step --attempt [--reason]` feeds it ([events](../interpreter.md#events), [running a task](../running.md#step-retry)).
It is valid only when the named attempt is the step's latest and finished `failed` or `stopped`; the engine then starts the next attempt through the same path as a first attempt.
Naming the attempt, rather than only the step, makes the decision about one outcome: a retry of attempt 1 that arrives after attempt 2 started is a replay that records and launches nothing beyond a reason an interrupted retry had not yet sent, and one aimed at an older attempt is refused, so a stale or repeated command cannot start two attempts.
Refusals use the engine's `ErrEvent`, as every event that does not match the records does; the engine has no `ErrInvalid` of its own.

The reason, when given, is sent as Director message `<repo>.<task>.attempt-retried.<step>-<n>` only after the next attempt is recorded, under an id derived from the retried attempt.
A retry refused before then sends nothing, so the Director can repeat it with another reason.
`step retry` refuses a step whose adapter [`runtimes.Names`](../contracts/adapter-runtimes.md) does not list before the engine records anything.
The engine records the next attempt before launching it, so a launch refusal would otherwise leave that attempt and its reason stored, and a later retry with another reason would be ignored as a replay.
A crash between recording the next attempt and sending its reason is recovered by replaying the retry, which resends the reason under the same id; a replay with a different reason conflicts with the message already sent and is ignored.
It is optional because a retry after a killed launcher needs no explanation, and a required one would be filled with noise.

A failed or stopped attempt no longer counts as having served loop feedback: the attempt that retries a looped build is given the reviewer's feedback again, where it would otherwise lose it.

Overloading `task start` with a flag was rejected: `task start` feeds `task-created`, which means the task was created, and the reason would go unrecorded.
