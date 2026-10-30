# Hold until merged

The s21 notes review found four Director notes (n11, the second half of n14, n15 and n16) of one form: "once task X merges, do Y".
They existed only because Guildhall could not hold an item until another task merged, and a note carries no wake and never comes back by itself.
[0092](0092-park-attempt.md) deferred exactly this: `--until <repo>/<task>` on a park, with the note that a release without a wake would hide the end of the hold.

Three kinds of item can now be held until named tasks merge ([holds](../director.md#holds)):

- a parked attempt, `step park … --until-merged <repo>/<task>`;
- an open decision, `decide defer … --until-merged <repo>/<task> --reason <text>`, lifted by `decide undefer`;
- a follow-up with no attempt or decision yet, `director follow-up add --until-merged <repo>/<task> <text>`, ended by `director follow-up done`.

`--until-merged` may be repeated, and an item returns once all the named tasks have merged (all-of), or at once, marked `dependency cancelled`, when any of them is cancelled.
On release the item comes back in `needs_me` with its reason and what released it, and the Director gets one wake; nothing is retried, answered or created automatically.

## Release is derived

`director status` judges each hold from the task states it already reads in `DirectorView`, with one pure function, `store.JudgeHold`: released when every named task is `done`, `dependency-cancelled` when any is `cancelled`, otherwise waiting for the named tasks still open.
No release record is written, so:

- a hold is released with no supervisor running, at the next status read, the "status could judge it expired with a store read" half of 0092;
- cancellation needs no hook of its own: a cancel command only sets the task's state, which status already reads;
- the engine never reads a hold: `internal/interp` and its `Records` interface are unchanged, as 0092 keeps parks out of the engine and the dependency override keeps its record out too.

The release's time is the commit time of the dependency's `set-task-state` receipt, which the feed already reads.

A held park counts, exactly as a park without the flag, while its hold waits, and stops counting once released, so the existing `retry` item returns with the release and the park reason appended; `step retry` and `step unpark` still clear it as before.
A deferred decision is left out of `needs_me` and of the Lanterns decision count while it waits, and is an ordinary `decision` item once released; the decision record itself never changes, so it stays open and other tasks' dependency states, which name an open decision, do not shift.
A released follow-up is a new `needs_me` kind, `follow-up`, after `decision` and before `messages`; unlike `decision`, it waits on the Director, so the resume wake and the Lanterns attention count include it.

## The wake comes from a supervisor watcher

0092 required that the end of a hold wake the Director.
A wake sent from the merge step itself would be immediate, but it would need a new engine read of Director records, would run under the merging task's lock and would miss cancellation entirely.
Instead a fifth watcher of the [checks watcher](0086-director-checks-watch.md)'s kind runs every `checks_refresh` while the session is `running` or `recovering`: it reads status and, for each released hold that still matters, sends one `hold-released` message, sender `supervisor`, and wakes the Director for it.
The message id is derived from the hold and its cycle, `hold-released.<kind>.<repo>.<task>.<item>.<cycle>` (a follow-up, done once, uses `hold-released.follow-up.<id>`), and a message already sent is not sent again, so each release wakes once however many rounds see it, and a park or deferral recorded again after an unpark or undefer wakes again when it is released.
The dependency's own `merged` message already wakes the Director at the moment it merges, so the watcher's delay of up to one `checks_refresh` adds only the reminder of what that merge released.

## Per-kind records

One generic hold record keyed by item was considered.
Parks already exist with their own cycle-carrying mutation ids and tombstones, and a decision and a follow-up have different keys, lifecycles and writers: a follow-up is a Director-level record with its own id sequence, written only by the newest session as notes are, while a park and a deferral are per-repository records keyed by what they hold.
So `AttemptPark` gains `until_merged`, and two new lazily created buckets, `decision-deferrals` per repository and `follow-ups` in the director bucket, hold the other two; the store schema stays at version 1.
They share only what is the same: the check made when any hold is recorded, inside its write transaction, that it names at least one task, each an open task of this store, none twice and none the held item's own; and `JudgeHold`.

A named task already done or cancelled is refused, `<repo>/<task> already merged; act now instead`, rather than released at once, so the Director acts now instead of recording a hold that is over before it starts.

## Not changed

Director notes are unchanged and are not migrated: after this ships the Director moves n11, n14 (second half), n15 and n16 into holds by hand and removes the notes.
Named tasks must be in the same store, as `--depends-on`'s are.
