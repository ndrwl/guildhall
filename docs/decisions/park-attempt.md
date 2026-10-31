---
status: accepted
date: 2026-10-29
topic: tasks
tasks: d15
amended-by: hold-until-merged.md
formerly: D92, 0092-park-attempt.md
---

# The Director parks a failed or stopped attempt with a reason, so it leaves needs_me

## Context

Every failed or stopped attempt of an open task put two items in `director status`'s `needs_me`: a `retry` item for the attempt and a `director` item for the task's `next.json`, `{"wait":"director","step":<s>,"attempt":<n>}`.
The Director had no way to say "I am deliberately not retrying this yet", so a held attempt, such as d4's review held after three rejections until d11 merged, woke every `director resume`, filled every startup snapshot and counted as Lanterns attention for as long as it was held.

## Decision

`guildhall step park --repo <r> --task <t> --step <s> --attempt <n> --reason <text>` records that hold, and `guildhall step unpark` lifts it ([running](../running.md#step-park-and-step-unpark)).

- **A record per attempt.**
  A park lives in a per-repository `attempt-parks` bucket created lazily, so the store schema stays at version 1, as the [dependency override](../tasks.md#dependencies) did.
  It holds the `reason`, whether the attempt is `parked`, and a `cycle`, the number of unparks so far; unpark keeps the record as a tombstone with `parked` false and the cycle one higher.
- **A Director record only.**
  Parking never changes the attempt's outcome, `next.json` or the engine, answers no decision, sends no message, takes no task lock and starts nothing; the engine never reads it.
  The store refuses, inside the park's write, a blank or oversized reason, a closed task, the judge step, an attempt that is not its step's latest and one running or succeeded; unpark refuses only an attempt that is not parked, so a stale park can always be cleared.
- **Mutation ids carry the cycle**, `park.<repo>.<task>.<step>.<n>.<cycle>` or `unpark.…`, hashed when over the store's 128 bytes.
  An identical park replays, a park with another reason while parked is refused quoting the prior reason, and park, unpark, park records a new park under cycle 1; the store checks the cycle the CLI read inside the write, so a park or unpark between the read and the write is refused, not lost.
- **Clearing is derived.** `director status` counts a park only while it is parked, its task is open and its attempt is still the step's latest, failed or stopped, so `step retry` clears it by recording attempt `n+1`, with no write of its own; a retry refused before `n+1` exists leaves the park in force.
- **What a counting park hides.**
  It leaves out the attempt's `retry` item and the task's `director` item when `next.json` waits on that same attempt with no decision.
  A lost `advance` item, a `director` wait for a decision or another attempt and a `paused` item stay: a retry the Director asked for while paused wins over a park.
  The attempt is listed in a `parked` section of the feed, with its reason, when it was parked and the `step retry` command; its step is marked `parked`; and a new session's startup instructions list it as information rather than work ([the Director's start](director-start.md)).

## Alternatives rejected

- A mutation id derived only from the attempt: it would replay the old park after an unpark and record nothing.
- `--until <repo>/<task>`, unparking when another task merges, judged expired by a status read alone: putting the attempt back in `needs_me` without waking the Director would hide the end of the hold, and a wake at merge time needs a supervisor watcher or a merge-time message, so it was deferred.

## Consequences

The readers of `needs_me` (the resume wake, the Lanterns band and the startup in-flight list) take it as it is, so they needed no change.
The deferred hold until another task merges is now built as `--until-merged`, with a supervisor watcher's wake ([hold until merged](hold-until-merged.md)).
Whether the Lanterns band should count long holds separately is left open.
