---
status: accepted
date: 2026-10-30
topic: tasks
tasks: d52
amends: park-attempt.md
formerly: D96, 0096-hold-until-merged.md
---

# A parked attempt, a deferred decision or a follow-up can be held until named tasks merge

## Context

The s21 notes review found four Director notes (n11, the second half of n14, n15 and n16) of one form: "once task X merges, do Y".
They existed only because Guildhall could not hold an item until another task merged, and a note carries no wake and never comes back by itself.
[Parking](park-attempt.md) deferred exactly this, `--until <repo>/<task>`, noting that a release without a wake would hide the end of the hold.

## Decision

Three kinds of item can be held until named tasks merge ([holds](../director.md#holds)): a parked attempt, `step park … --until-merged <repo>/<task>`; an open decision, `decide defer … --until-merged <repo>/<task> --reason <text>`, lifted by `decide undefer`; and a follow-up with no attempt or decision yet, `director follow-up add --until-merged <repo>/<task> <text>`, ended by `director follow-up done`.

- **All-of, and cancellation releases.** `--until-merged` may be repeated; an item returns once all the named tasks have merged, or at once, marked `dependency cancelled`, when any is cancelled.
  On release it comes back in `needs_me` with its reason and what released it, and the Director gets one wake; nothing is retried, answered or created automatically.
- **Release is derived.** `director status` judges each hold from the task states `DirectorView` already reads, with one pure function, `store.JudgeHold`, and writes no release record.
  A hold is therefore released with no supervisor running, at the next status read; cancellation needs no hook of its own; and the engine never reads a hold, so `internal/interp` and its `Records` are unchanged.
  The release's time is the commit time of the dependency's `set-task-state` receipt.
- **How each kind returns.**
  A held park counts as any park while its hold waits and stops counting once released, so its `retry` item returns with the park reason appended.
  A deferred decision is left out of `needs_me` and the Lanterns decision count while it waits; the decision record never changes, so other tasks' dependency states do not shift.
  A released follow-up is a new `needs_me` kind, `follow-up`, after `decision` and before `messages`, counted by the resume wake and Lanterns attention.
- **The wake comes from a supervisor watcher**, a fifth of the [checks watcher](director-checks-watch.md)'s kind, every `checks_refresh` while the session is `running` or `recovering`: it sends one `hold-released` message, sender `supervisor`, per released hold, id `hold-released.<kind>.<repo>.<task>.<item>.<cycle>` (or `hold-released.follow-up.<id>`), so each release wakes once and a hold recorded again after an unpark or undefer wakes again.
- **Per-kind records.**
  `AttemptPark` gains `until_merged`, and two lazily created buckets, `decision-deferrals` per repository and `follow-ups` in the director bucket, hold the others; the store schema stays at version 1.
  They share only the check made when any hold is recorded, inside its write (at least one task, each an open task of this store, none twice and none the held item's own), and `JudgeHold`.
- A named task already done or cancelled is refused, `<repo>/<task> already merged; act now instead`, so the Director acts now rather than record a hold that is over before it starts.

## Alternatives rejected

- A wake sent from the merge step: immediate, but it would need a new engine read of Director records, would run under the merging task's lock and would miss cancellation entirely.
- One generic hold record keyed by item: parks already have their own cycle-carrying mutation ids and tombstones, and a decision and a follow-up have different keys, lifecycles and writers (a follow-up is a Director-level record with its own id sequence, written only by the newest session, as notes are).

## Consequences

The dependency's own `merged` message already wakes the Director when it merges, so the watcher's delay of up to one `checks_refresh` adds only the reminder of what that merge released.
Director notes are unchanged and not migrated: the Director moves n11, n14 (second half), n15 and n16 into holds by hand and removes the notes.
Named tasks must be in the same store, as `--depends-on`'s are.
