# Parking a failed or stopped attempt

Every failed or stopped attempt of an open task put two items in `director status`'s `needs_me`: a `retry` item for the attempt and a `director` item for the task's `next.json`, `{"wait":"director","step":<s>,"attempt":<n>}`.
The Director had no way to say "I am deliberately not retrying this yet", so a held attempt, such as d4's review held after three rejections until d11 merged, woke every `director resume`, filled every startup snapshot and counted as Lanterns attention for as long as it was held.

`guildhall step park --repo <r> --task <t> --step <s> --attempt <n> --reason <text>` now records that hold, and `guildhall step unpark` lifts it ([running](../running.md#step-park-and-step-unpark)).

## The record

A park is a store record per attempt, in a per-repository `attempt-parks` bucket created lazily, so the store schema stays at version 1, as the [dependency override](../tasks.md#dependencies) did.
It is keyed by the attempt, like the attempt record, and holds the `reason`, whether the attempt is `parked`, and a `cycle`: the number of unparks so far.
Unpark keeps the record with `parked` false and the cycle one higher, a tombstone, so the cycle survives.

Parking is a Director record only.
It never changes the attempt's outcome, `next.json` or the engine, answers no decision, sends no message, takes no task lock and starts nothing; the engine never reads it.
The store refuses, in the park's own write transaction, a blank or oversized reason, a closed task, the judge step, an attempt that is not its step's latest and one that is running or succeeded; unpark refuses only an attempt that is not parked, so a stale park can always be cleared.

## Mutation ids carry the cycle

A mutation id replays its first receipt, so an id derived only from the attempt would make a park after an unpark replay the old park and record nothing.
The CLI reads the record first and derives the id from its cycle, `park.<repo>.<task>.<step>.<n>.<cycle>` or `unpark.…`, hashed when longer than the store's 128 bytes.
An identical park therefore replays, a park with another reason while parked is refused quoting the prior reason, and park, unpark, park records a new park under cycle 1.
The store checks the cycle the CLI read inside the write, so a park or unpark between the read and the write is refused, not lost.

## Clearing is derived

`director status` counts a park only while it is parked, its task is open and its attempt is still the step's latest, failed or stopped.
`step retry` therefore clears a park by recording attempt `n+1`, with no write of its own and no engine change; a retry refused before `n+1` exists leaves the park in force, since nothing was retried.

A counting park leaves out the attempt's `retry` item and the task's `director` item when `next.json` waits for the Director on that same attempt with no decision.
A lost `advance` item stays, because the failure was never taken, as do a `director` wait for a decision or another attempt and a `paused` item: a retry the Director asked for while paused wins over a park.
The attempt is listed instead in a `parked` section of the feed, with its reason, when it was parked and the `step retry` command, its step is marked `parked`, and a new session's startup instructions list it, from the same status read, as information rather than work ([D71](0071-director-start.md)).
The readers of `needs_me`, the resume wake, the Lanterns band and the startup in-flight list, take it as it is, so they needed no change.

## Deferred

`--until <repo>/<task>`, unparking automatically when another task merges, is a follow-up task: status could judge it expired with a store read, but putting the attempt back in `needs_me` without waking the Director would hide the end of the hold, and a wake at merge time needs a supervisor watcher or a merge-time message.
Whether the Lanterns band should count long holds separately is left open.
