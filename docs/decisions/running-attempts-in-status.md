---
status: accepted
date: 2026-10-30
topic: director
tasks: d54
---

# The status feed and startup list every running attempt as information, without a new probe or `needs_me` kind

## Context

After a restart the Director saw an in-flight Artisan attempt only when `needs_me` flagged it, as a `resume` item for a stuck, exited or unknown launcher.
A healthy attempt never appeared in `startup.md`, so checking that each was alive meant running `guildhall artisan status` by hand for each one.
Yet `director status` already classified every attempt with no store outcome with the same `launch.Classify` that `artisan status` uses, and kept only its `state` and `reason`.

## Decision

- `director status` gains a `running` list ([status](../director.md#status)): every attempt it classifies, other than a [Director stage](../interpreter.md#director-stages), with its state, reason, launcher and endpoint evidence, the last heartbeat's `worker_state`, `heartbeat_at` and `last_output_at`, and the `guildhall artisan status` command.
  Times are absolute, not ages, so the feed is readable and its golden output deterministic.
  It is not a `needs_me` kind, so `director resume`'s wake count and every reader of `needs_me` are unchanged.
- `startup.md` gains a `## Running` section between `In flight` and `Parked`, built from the same status read ([session start](director-start.md)), one line per attempt with its heartbeat and output ages by the supervisor's clock, left out when nothing runs; the startup skill asks the Director to confirm it before `director ready`.
- `launch.ClassifyAt` takes the caller's clock, and `director status` passes its own, so heartbeat age and the `stuck` decision follow it; `artisan status` keeps the wall clock.
- A non-judge attempt of an open task whose files say `finished`, `failed` or `cancelled` but whose store outcome is empty, as a launcher that died between writing `outcome.json` and recording it leaves it, is a `resume` item, as a judge's already was; `artisan resume` settles it from the existing outcome.
  A Director stage's published result stays an `advance` item and is not also a `resume` one.

## Alternatives rejected

- A second probe: another `artisan status` pass would double the lock and endpoint waits, up to four seconds an attempt, on every Director turn.
- Flagging `absent`: an attempt is `absent` for a moment after every advance, before its launcher takes ownership, and a launch that never happened is already recovered by `task start` or the `paused` path; it is listed under `running` instead.
- A separate no-output threshold: a live launcher already sends `worker-stalled` at `stall_interval` and a gone launcher is `exited`, so a threshold of its own would give two items for one attempt; the Running line shows the last output's age instead.

## Consequences

The change is additive to the feed's format.
Attempts on done or cancelled tasks gain no `needs_me` item, as before, but are listed under `running` while their store outcome is empty, because a live launcher on a closed task is worth seeing.
