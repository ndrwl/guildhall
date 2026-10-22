# D64. The per-task watcher is the launcher's last act

Until now the caller ran `step done` once an attempt's result existed, and nothing noticed an attempt that failed or was stopped until the Director next ran `task start` ([D52](0052-task-start-and-step-done.md)).
The Director should receive results and failures without typing either command (R1.29), and its wake (slice 4, D5) rides on the messages those advances send.

The owned launcher already outlives the command that started it and is the one process that knows when its attempt ends, so it is the watcher: after it records the attempt's store outcome, `launch.Run` runs `launch.Config.Advance` as a child.
After a finished attempt with a valid result, succeeded or failed, that is `guildhall step done --copy <dir> --repo --task --step --attempt --result <file>`; after any other outcome it is `guildhall task start --copy <dir> --repo <repo> <task>`, so the engine finds the failed or stopped attempt and sends the Director `attempt-failed`.
No new process, daemon or poll is added, and the advance is the one production engine assembly the commands already run, not a second copy inside the launcher (hallmark 9: shell out to `step done`).

The advance runs only after the outcome is published and recorded, and the ownership released, so it can never act before the marker decision of a cancel, and an advance that launches the step's next attempt finds this one finished.
Its failure never changes the attempt's outcome or `Run`'s result: it is logged, the child's output in `launcher.log` and an `advance` event with its exit status in `events.jsonl`.
Both commands are idempotent and the Director's next `task start` repeats the same advance ([replay and recovery](../interpreter.md#replay-and-recovery)), so a lost advance is recovered rather than retried by the launcher.

`launch.Process` passes `--copy <dir> --advance` to `artisan start` only when the engine runs from a [copy](../running.md#copies), because the advance needs the config and baseline that only a copy names in one flag; `artisan start` refuses `--advance` without a copy, and run by hand without it behaves as before.
The copy's store and state must be the ones `--store` and `--state` name, so the launcher and its advance cannot disagree about the Director.
A Herdr-tab attempt has no owned launcher and takes no `--advance`.

`artisan resume` settles an attempt whose launcher is gone and does not advance; the Director's `task start` after it does, as before.
Every advance the command line runs, the watcher's or one typed by hand, writes what the task now waits for, the line it prints, to `next.json` beside the task lock while it still holds the lock, so [`director status`](../director.md#status) reads where each task last stood without the launcher keeping state of its own.
