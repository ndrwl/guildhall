# The store owns "the first finish stands"

Three writers finish an attempt, each under its own mutation id: the launcher when it decides the attempt, the interpreter's result intake when it takes the result, and `artisan resume` settling an exited launcher's attempt; any may write first ([task start and step done](0052-task-start-and-step-done.md)).
The rule that the first record stands, and that an equal second record is not an error, was written in the launcher and the intake and absent from resume, so the copies already differed and a resume losing the race exited 1 on a finished attempt (sustainability review 8, F22).

`store.FinishAttempt` now owns it ([attempts](../store.md#attempts)): an equal outcome under another mutation id returns the receipt of the finish that stands and writes nothing, and another outcome is refused with `ErrInvalid` as before.
The tolerance is deleted from `launch.Run` and the intake, `Resume` needed no edit, and a later writer, such as the watcher or a Director command, inherits the rule; changing it, for example to prefer the launcher's outcome, changes one place.

Returning the earlier receipt rather than a new one keeps a receipt meaning a committed write and leaves the second id free.
The earlier receipt is found by scanning receipts for the operation and request digest only when a second writer arrives; this also identifies finishes committed before receipts named their record.
Recording the finishing mutation id on the attempt would avoid the scan but change the attempt record's format for a path taken at most twice per attempt.
