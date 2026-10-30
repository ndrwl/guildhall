# Running attempts in the status feed and startup

After a restart the Director saw an in-flight Artisan attempt only when `needs_me` flagged it, as a `resume` item for a stuck, exited or unknown launcher.
A healthy attempt never appeared in `startup.md`, so checking that each was alive meant running `guildhall artisan status` by hand for each one.
Yet `director status` already classified every attempt with no store outcome with the same `launch.Classify` that `artisan status` uses, and kept only its `state` and `reason`.

## What changed

- `director status` gains a `running` list ([status](../director.md#status)): every attempt it classifies, other than a [Director stage](../interpreter.md#director-stages), with its state, reason, launcher and endpoint evidence, the last heartbeat's `worker_state`, `heartbeat_at` and `last_output_at`, and the `guildhall artisan status` command.
  Times are absolute, not ages, so the feed is readable and its golden output deterministic.
  It is not a `needs_me` kind, so `director resume`'s wake count and every reader of `needs_me` are unchanged; the change is additive.
- `startup.md` gains a `## Running` section between `In flight` and `Parked`, built from the same status read ([D71](0071-director-start.md)), one line per attempt with its heartbeat and output ages by the supervisor's clock; it is left out when nothing runs.
  The startup skill asks the Director to confirm it before `director ready`.
- `launch.ClassifyAt` takes the caller's clock, and `director status` passes its own, so heartbeat age and the `stuck` decision follow it; `artisan status` keeps the wall clock.
- A non-judge attempt of an open task whose files say `finished`, `failed` or `cancelled` but whose store outcome is empty, as a launcher that died between writing `outcome.json` and recording it leaves it, is now a `resume` item, as a judge's already was; `artisan resume` settles it from the existing outcome.
  A Director stage's published result stays an `advance` item and is not also a `resume` one.

## Not done

- **No second probe.** The feed reuses the classification it already makes; another `artisan status` pass would double the lock and endpoint waits, up to four seconds an attempt, on every Director turn.
- **`absent` is not flagged.** An attempt is `absent` for a moment after every advance, before its launcher takes ownership, and a launch that never happened is already recovered by `task start` or the `paused` path. It is listed under `running`, so the Director can see it.
- **No separate no-output threshold.** A live launcher already sends `worker-stalled` at `stall_interval`, a `stalled` item, and a gone launcher is `exited`, a `resume` item; a threshold of its own would give two items for one attempt. The Running line shows the last output's age instead.
- Attempts on done or cancelled tasks gain no `needs_me` item, as before, but are listed under `running` while their store outcome is empty: a live launcher on a closed task is worth seeing.
