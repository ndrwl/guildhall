# The supervisor's checks watcher and the resume wake

The Sol cross-review of the Director (X1 and X5) found two places where work waited with nothing owned to move it on.
A task waiting at [validate](../interpreter.md#validate) for pending checks was advanced only by a `task start` someone happened to run, so a pull request whose CI turned green after the watcher's advance stayed at validate indefinitely.
A `director resume` run from another terminal moved the session to `running` and printed `needs_me` to its caller, but typed nothing into the Director's pane, so messages queued while it was paused and tasks held by the pause waited for an unrelated wake.
The captain's goal is a Director that spends no model turns while idle, and supports repositories with and without CI, re-reading checks on the configured refresh.

## Decisions

- **The session supervisor owns the checks continuation.**
  While the runtime runs, `director start`'s supervisor already reads its session about once a second; on the same tick it runs a checks watcher.
  The [watcher's cadence and minimum interval](../director.md#checks) keep a `"0s"` refresh from reading the forge at every tick, with no model turn.
  The advance reads the forge under the engine's existing pacing and sends what changed; its `checks-pending` message is head-derived, so a re-read of still-pending checks sends and wakes nothing new, and passed or failed checks send `checks-passed` or `checks-failed`, which wake the Director as any send does.
  The supervisor is not a service: it lives exactly as long as the Director's session (R1.29).
- **One round at a time, failures retried.**
  A round that is still advancing when the next tick comes starts no second round, and a failed advance, such as an unreachable forge, is logged to the session's `checks.log` and tried again a refresh later.
  The supervisor waits for a running round before it records the session stopped.
- **The watcher follows the hold.**
  While the session is `paused` or `stopping` the watcher advances nothing, as `task start` starts nothing; pause stops new work only.
- **The feed shows the wait.**
  `needs_me` lists a task waiting for checks as a `checks` item, last, with the `task start` that reads them again, so a person or a Director with no supervisor can see it and move it on; it is not the Director's to act on while its supervisor runs.
- **Resume wakes once, under its own identity.**
  A `director resume` that moves the session to `running` uses a separate [resume wake](../director.md#pause-and-resume) for actionable work rather than replaying message sends.
  It records nothing in the store: a resume is not a message, the queued wake each message recorded while paused stands unchanged, and per-message wake idempotency is kept.
  A repeated resume of a running session moved nothing and types nothing; a resume with nothing waiting types nothing.

## Not yet

Forge events reaching the Director's machine, which would let the watcher wait for an event rather than re-read on a timer, belong to managed mode.
