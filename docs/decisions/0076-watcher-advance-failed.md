# D76. A failed watcher advance is told and shown

[D64](0064-launcher-advances-its-task.md) made the attempt's launcher the task's watcher and only logged a failed advance, leaving the Director's next `task start` to recover it.
Sustainability review 11 (F35) found that silent: an advance that loses the task lock to another advance of the same task, such as the Director's own `task start` while the attempt ends, waits 30 seconds and exits 1; the finished attempt then sits with a stale `next.json`, no message, no wake and an empty `needs_me` until someone happens to run `task start`.
D10's stop and D12's caps make that collision routine, and the slice 4 goal is that events wake the Director, with no polling.

This amends D64's failure rule; everything else in D64 stands.

## Once more, then tell the Director

An advance that exits unsuccessfully is run once more, which waits the task lock's wait again, so a lock held for less than about a minute is absorbed without a message.
When the second run fails too, the launcher sends the Director `<repo>.<task>.advance-failed.<step>-<n>`, type `advance-failed`, naming the command and its last line of error output (`interp.AdvanceFailedMessage`).
The send goes through the launcher's outbox, the store opened for the one write with no task lock, and wakes the Director as every send does, so the failure that a lock caused cannot itself be lost to the lock.
The id is one per attempt, so a resent message is a replay.
The attempt's outcome and the launcher's exit status are unchanged, as before.

Waiting for the lock without bound was rejected: a held lock would become a launcher that never exits, and an attempt `artisan stop` cannot settle.

## Shown in the status feed

The message is read once; the state it describes lasts until an advance takes the attempt.
So `director status` derives an `advance` item from the records rather than from the message: an open task whose latest attempt of a step is finished while the task's newest `next.json` still says it waits on that attempt.
Its command is `step done` with the result file for a succeeded attempt whose result file exists, and `task start` otherwise, and it comes first in `needs_me`, before `retry`, because nothing else advances such a task.
