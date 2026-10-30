# Director maintenance

In session 21 the Director and the Guildmaster went through every Director note by hand: they removed the notes the store already held and turned recurring kinds of note into tasks.
The Guildmaster asked for this to happen regularly, as part of a maintenance procedure.
Hallmark had the same weakness: "after every three or four merged pull requests" happened only when the Director remembered.
The unattended grill's five questions were accepted as recommended, with one change at the first spec review: count only what happens after the upgrade.
Director maintenance is now one skill, `guildhall-maintenance`, with two parts, the note audit and the hallmark; Guildhall says when each is due and each run is recorded ([maintenance](../director.md#maintenance)).

## Decisions

- **A `needs_me` item, not a wake suffix.**
  A handoff due rides on the wake line and is recorded once per crossing; the grill asked for "a maintenance due item in needs_me (like handoff due)", which could be only one of the two.
  The Guildmaster chose a `maintenance` item in `needs_me` only, computed on each status read with nothing recorded, and no wake-line suffix.
  Maintenance is not urgent: the item is enough for the Director to see it in `director status` and `startup.md` without remembering, it does not wake the Director on resume and it does not hold back `director ready`.
- **Counting starts at the upgrade.**
  The baseline is the start of the earliest session recorded with a maintenance policy: in an upgraded copy the first session started by a release with maintenance, in a fresh store session 1.
  With no run recorded, sessions and merges count from the baseline and never from older history, so an upgrade shows no item at once, whatever the merge history; the first hallmark is due after 4 merges made after it and the first note audit 3 sessions after it.
  The baseline needs no record of its own: it is read from the sessions.
  A session started with both parts off records no policy, so a copy that never turned maintenance on gets its baseline when it first does.
- **Cadences are `[director]` settings recorded on the session.**
  `note_audit_every_sessions` (3) and `hallmark_after_merges` (4), 0 for off, follow the handoff triggers: validated as non-negative integers and recorded on each session as it starts, so a change applies from the next session.
  The hallmark counts `merged` messages per repository, since a hallmark reviews one repository; `FinishedTasks` counts closes of every repository, cancellations among them, so it is not the count.
- **Each run is a small record.**
  `director maintenance record` keeps a run in the store's `director-maintenance` bucket with its kind, a hallmark's repository and task, the session, the time and a one-line summary of at most 1024 bytes, what was removed and what was proposed, written only by the newest session that is not stopped, as notes are ([D94](0094-director-notes.md)).
  A hallmark is recorded when its task is launched, so merges made while it runs count toward the next one.
  Superseded for the hallmark by [engine-owned hallmark cadence](engine-owned-hallmark-cadence.md): a repository's last hallmark is its last done hallmark task, counted from that task's launch, `record --hallmark` is refused, and hallmark runs recorded before are read only for a repository with no done hallmark task.
- **The status feed carries counts and times, never note text.**
  `director status` gains a `maintenance` object with the last note audit and sessions until the next, and per repository the last hallmark and merges until the next; D94's "notes are not in the feed" still holds, and `director maintenance list` shows the history.
- **The Guildmaster stays in the loop.**
  The Director removes, edits and compacts notes on its own, each removal with a reason naming the record that holds it; proposed tasks and procedure changes go to the Guildmaster as a short list, and nothing is created from it before they approve.

## Not changed

Notes are never removed automatically; handoff triggers, the wake line and `handoff-dues` are untouched; a hallmark stays an Artisan task the Director launches, never one Guildhall creates; the `[quality]` settings and `startup.md`'s 8 KB notes warning are unchanged.
