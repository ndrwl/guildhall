---
status: accepted
date: 2026-10-22
topic: engine
formerly: D59, 0059-stall-interval-setting.md, D70, 0070-launcher-stall.md
---

# Worker stall observation is on by default and the attempt's launcher makes it at each heartbeat

## Context

The engine's `worker-stalled` observation was off in production: `task start` and `step done` built the engine without `Engine.StallInterval`, and the setting had no home in the config (sustainability review 9, F26).
Once on, it still ran only during an advance, so a silent worker was reported only when something else advanced its task.

## Decision

- **A config setting.**
  The Director config's `stall_interval` sets the interval for every advance, read through `config.StallInterval` and validated by `config.Parse`; it is an ordinary setting under the existing precedence, so a Steward may manage it and a copy may set it under `[local]`.
  The default is 15 minutes, `"0s"` turns the observation off, and the value is a Go duration string, as the `artisan` commands' `--timeout` flags take.
- **The launcher observes its own worker.** `launch.Process` passes a positive interval to the launcher, which checks at each five-second heartbeat and sends the same `interp.StalledMessage`; its id comes from the attempt and `last_output_at`, so whichever of the launcher and the advance sends second replays the first.
  The advance's observation stays as the fallback for a launcher run without `--stall-interval` or whose send failed.
- **Off the supervision loop.** `artisan.LauncherConfig.Beat` hands each beat to `launch.Run`, which sends on its own goroutine with at most one waiting beat, because the store's lock and a Herdr pane may each wait and a stale heartbeat would read as a stuck launcher.
- **Output is the files' modification time.** `last_output_at` is the newest modification time of the worker's standard output and error when they are regular files, as `launch.Process` makes them by appending to `launcher.log`.

Behaviour detail is under [worker-stalled observations](../interpreter.md#worker-stalled-observations) and [heartbeat](../artisan.md#heartbeat).

## Alternatives rejected

- A bare number with an implied unit: the duration string matches the existing `--timeout` flags.
- A new process or polling by anyone else: the launcher already beats every five seconds.
- `interp` checking inside `artisan`: `artisan` cannot import `interp`, which imports it.
- A pipe the launcher copies the worker's output through: it would change the worker's handles from the file or terminal it is given, and a grandchild holding the pipe would keep the launcher's wait alive after the worker exits.

## Consequences

A worker writing to a terminal, as `artisan start` run by hand gives it, is not observed, and the launcher's own lines in `launcher.log` count as output.
So the launcher's wake failures are not written to its standard error, which would restart the worker's stall clock.
An attempt started by hand without `--stall-interval` observes nothing, as before.

## History

- 2026-10-22, D59 (0059-stall-interval-setting.md): turned the observation on in production through `stall_interval`, 15 minutes by default, as a Go duration string.
  Rejected then: a bare number with an implied unit.
- 2026-10-22, D70 (0070-launcher-stall.md): moved the observation into the launcher's heartbeat with one message and two senders, the check on its own goroutine and output measured from the files' modification times.
  Rejected then: a pipe copying the worker's output, and the check living in `artisan`.
