---
status: accepted
date: 2026-10-30
topic: maintenance
tasks: d57
amended-by: engine-owned-hallmark-cadence.md
---

# Director maintenance is one skill with a note audit and a hallmark, each due on a counted cadence and recorded

## Context

In session 21 the Director and the Guildmaster went through every Director note by hand: they removed the notes the store already held and turned recurring kinds of note into tasks.
The Guildmaster asked for this to happen regularly, as part of a maintenance procedure.
Hallmark had the same weakness: "after every three or four merged pull requests" happened only when the Director remembered.
The unattended grill's five questions were accepted as recommended, with one change at the first spec review: count only what happens after the upgrade.

## Decision

Director maintenance is one skill, `guildhall-maintenance`, with two parts, the note audit and the hallmark; Guildhall says when each is due and each run is recorded ([maintenance](../director.md#maintenance)).

- **A `needs_me` item, not a wake suffix.**
  Due maintenance is a `maintenance` item in `needs_me`, computed on each status read with nothing recorded.
  Maintenance is not urgent: the item is enough for the Director to see it in `director status` and `startup.md`, it does not wake the Director on resume and it does not hold back `director ready`.
- **Counting starts at the upgrade.**
  The baseline is the start of the earliest session recorded with a maintenance policy, read from the sessions with no record of its own; with no run recorded, sessions and merges count from it and never from older history, so an upgrade shows no item at once.
  A session started with both parts off records no policy, so a copy that never turned maintenance on gets its baseline when it first does.
- **Cadences are `[director]` settings recorded on the session.**
  `note_audit_every_sessions` (3) and `hallmark_after_merges` (4), 0 for off, follow the handoff triggers: non-negative integers recorded on each session as it starts, so a change applies from the next session.
  The hallmark counts `merged` messages per repository, since a hallmark reviews one repository; `FinishedTasks` counts closes of every repository, cancellations among them, so it is not the count.
- **Each run is a small record.**
  `director maintenance record` keeps a run in the store's `director-maintenance` bucket with its kind, the session, the time and a one-line summary of at most 1024 bytes, written only by the newest session that is not stopped, as [notes](director-reload-and-notes.md) are.
  For the hallmark this is amended by [engine-owned hallmark cadence](engine-owned-hallmark-cadence.md): the last hallmark is the repository's last done hallmark task, counted from the answer to its `follow-ups` decision (from its launch when it has none) and holding the next one until that answer, and `record --hallmark` is refused.
- **The status feed carries counts and times, never note text.**
  `director status` gains a `maintenance` object with the last note audit and sessions until the next, and per repository the last hallmark and merges until the next; `director maintenance list` shows the history.
- **The Guildmaster stays in the loop.**
  The Director removes, edits and compacts notes on its own, each removal with a reason naming the record that holds it; proposed tasks and procedure changes go to the Guildmaster as a short list, and nothing is created from it before they approve.

## Alternatives rejected

- A wake-line suffix recorded once per crossing, as a handoff due is: the grill asked for one of the two, and the Guildmaster chose the `needs_me` item only.
- Counting from older history: an upgrade would show items at once, whatever the merge history.

## Consequences

The first hallmark is due after 4 merges made after the baseline and the first note audit 3 sessions after it.
Notes are never removed automatically; handoff triggers, the wake line and `handoff-dues` are untouched; a hallmark stays an Artisan task the Director launches, never one Guildhall creates; the `[quality]` settings and `startup.md`'s 8 KB notes warning are unchanged; the [Director notes](director-reload-and-notes.md) rule that notes are not in the feed still holds.
Until [engine-owned hallmark cadence](engine-owned-hallmark-cadence.md), a hallmark run was recorded with its repository and task when the task was launched, so merges made while it ran counted toward the next one.
