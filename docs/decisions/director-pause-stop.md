# Pausing, resuming and stopping the Director

`guildhall director pause`, `director resume` and `director stop` are the Guildmaster's and firstmate's controls over a running Director session, following the [role grammar](0053-cli-role-grammar.md).
Each moves the newest [session record](director-session-record.md) and prints it.
The captain fixed the semantics: pause stops new work only and running attempts finish; stop also stops the Director's Artisan attempts.

## Decisions

- **Pause holds launches, not work.**
  A `paused` session starts no new attempt.
  `task start` and `step retry` check first and print `{"wait":"paused"}` with exit 0, recording nothing.
  `task start --decision` does not check first, so the Guildmaster's answer is still fed; `step done` still takes its result, and the engine's new `Engine.Hold` stops the next attempt from starting, so a launcher advancing its task during a pause does not start new work behind it.
  That advance waits `paused`, which its `next.json` records, and `needs_me` lists the task with the `task start` that starts the held attempt after resume.
  Running attempts, results, decisions and the Guildmaster's answers are unaffected.
- **Stopping holds launches too.**
  A `stopping` session holds launches as a paused one does, so no attempt starts after stop has stopped the running ones.
- **Resume is the Director's, not the attempt's.**
  `director resume` moves a paused session back to `running` and prints `needs_me` beside the session, which the Director then works.
  It is not `artisan resume`, which settles one attempt whose launcher is gone; the two share the lifecycle verb because the grammar gives every role the same four.
- **Stop ends the session and its attempts.**
  `director stop` records `stopping`, then stops every running attempt in the store through its launcher, as `artisan stop` does, and prints the session.
  The supervisor reads its session while the runtime runs; once it sees `stopping` it gives the runtime a grace period to end by itself, then ends it, and records `stopped`.
  `stop --now` records the same with `stop_now`, and the supervisor ends the runtime at its next read.
  A plain stop followed by `stop --now` shortens the grace.
- **A gone supervisor is recorded honestly.**
  When the session's supervisor identity is gone, `stop` records the session `stopped` with the end reason `superseded`, then stops its attempts.
  `pause` and `resume` are refused for it and name `director stop`.
- **The end of the turn waits for the handoff slice.**
  The planned end-of-turn signal, the handoff note and the `stop-requested` message that asks the Director to write one are the handoff slice's.
  Store messages belong to a task, so a message outside any task needs that slice's design.
  Until then, a plain stop's grace period stands in for the end of the turn, 60 seconds as the handoff design specifies for a runtime without a turn-end hook.

## Not yet

The `stop-requested` message and its wake, the handoff note, and ending the runtime at the end of its turn land with the handoff slice.
