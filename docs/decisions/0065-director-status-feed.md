# D65. The Director's status feed is one store read plus files

The Guildmaster asked to monitor the Director and to ask it "what needs me"; the Director needs the same answer at every turn and when a session starts after a crash.
Before this, answering took `task list`, a `task timeline` per task, `decide list`, `messages list` and an `artisan status` per running attempt, each opening the store separately, so the answer could mix store states.

`guildhall director status --json` ([the Director](../director.md#status)) answers in one document.
It reads the store once through `store.DirectorView`, one read transaction over every task, attempt, unreleased owner, open decision, unread Director message and task receipt, labelled with the newest sequence as `task timeline` is.
Only then does it read files: each task's `next.json` and each running attempt's directory, which `launch.Classify` classifies from the ownership record already read; `launch.Status` became `Classify` after its own store read, so the two cannot drift.
The store stays read-only and the read waits for a writer's lock as advances do, because the Director runs status while launchers and advances write.

`next.json` is copied as JSON exactly as written rather than decoded into a type, because the per-task watcher that writes it lands later and owns its shape; the feed interprets only `wait` to list a task waiting for the Director.
A file that is not JSON is reported beside the task rather than failing the feed, so one damaged file does not hide every other task.

`needs_me` gives each item the command the Director runs next, spelt without `--copy` because a Director session sets `GUILDHALL_COPY`.
The order is fixed: retries, resumes, stalled workers, decisions, other messages, then tasks waiting on the Director, so the same store state always lists the same items in the same order.
Unread messages other than `worker-stalled` are one counted item rather than one item each: the retry and decision items already cover the `attempt-failed` and `decision` messages the engine sends, and listing them again would double the list.
Only open tasks contribute retry, resume and wait items; a done or cancelled task has nothing left to act on.

`recent` drops each receipt's request digest, which only replay checks need and which, for an owned attempt, depends on the launcher's process identity.
The session section reports `no session` and the mode `standalone` until the session record and managed mode exist, so consumers can read those fields now and gain detail later without a format change.
