# The launcher observes its own worker's stall

The engine's `worker-stalled` observation ran only during an advance, so a silent worker was reported only when something else advanced its task (slice 4 plan, D4).
The launcher already writes a heartbeat every five seconds; it now makes the same observation at each beat and sends the same message, with no new process and no polling by anyone else.

## One message, two senders

`interp.StalledMessage(key, lastOutput)` is the message, and the engine's advance and the launcher both send it.
Its id is derived from the attempt and `last_output_at` exactly as the engine derived it before, so whichever sends second replays the first and the Director sees one message per crossing.
The advance-time observation stays as the fallback for a launcher run without `--stall-interval` or whose send failed.

## Where the check runs

`artisan` cannot import `interp`, which imports it, so `artisan.LauncherConfig.Beat` hands each written beat to the caller and `launch.Run` makes the check.
The send goes through the store and then the wake, and either may wait: the store's lock for up to 30 seconds, a Herdr pane for up to ten.
It therefore runs on its own goroutine, given at most one waiting beat, so the supervision loop never waits for it and a slow send cannot make the heartbeat stale, which reconcile would read as a stuck launcher.

## What counts as output

`last_output_at` was always when `Run` began, since nothing reported the worker's output.
The launcher now reports the newest modification time of the worker's standard output and error when they are regular files, as `launch.Process` makes them by appending both to `launcher.log`.
Wrapping them in a pipe the launcher copies would observe every write, but would change the worker's handles from the file or terminal it is given to a pipe, and a grandchild holding the pipe open would keep the copy, and so the launcher's wait, alive after the worker exits.
A worker writing to a terminal, as `artisan start` run by hand gives it, is therefore not observed, and the launcher's own lines in `launcher.log` count as output.
For the same reason the launcher's wake failures are not written to its standard error: the line would restart the worker's stall clock and report a new crossing an interval later.

## Defaults

`launch.Process` passes the engine's interval only when it is positive, and `artisan start` observes nothing without it, so an attempt started by hand behaves as before.
