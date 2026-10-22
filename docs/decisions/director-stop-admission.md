# Holding admission, stopping unowned attempts and containing the Director's runtime

A cross-review of the Director found three gaps between `director pause` or `director stop` and the work it should hold or end.
An advance read the hold, then recorded its attempt in a later transaction, so a pause or stop committed between the two still let it start.
`director stop` asked each unfinished attempt's owner to stop it, and an attempt with no owner, a Director stage or an Artisan attempt recorded and not yet launched, was reported stopped while it stayed unfinished, held running capacity and could still be launched.
The supervisor signalled only the runtime's own process, so a shell or tool the runtime had started outlived a stop that reported success.
The captain's semantics stand: pause stops new work only and running attempts finish; stop also stops the Director's attempts.

## Decisions

- **The hold is read where the attempt is recorded.**
  `store.AttemptCaps.Hold` makes `StartCappedAttempt` refuse with `store.ErrDirectorHeld`, writing nothing, while the newest Director session holds new attempts (`DirectorSession.Holds`), in the same transaction as the cap count and the start.
  A start therefore either commits before a pause or stop, and is seen by the stop's sweep, or is refused; the engine treats the refusal as `Engine.Hold` and waits `paused`.
- **A stop holds admission until its sweep has finished.**
  The supervisor may record the session `stopped` before `director stop`'s sweep is done, as `stop --now` does at once, and a session whose supervisor is gone used to be recorded `stopped` before its sweep began; either lifted a hold read from the state alone while an attempt's advance, or a tool the runtime left, could still start one.
  `RequestDirectorStop` therefore sets the session's durable `Sweeping`, and `director stop` clears it with `FinishDirectorStop` only once every attempt is proven stopped; `Holds` is `paused`, `stopping` or `Sweeping`, so `held` and the start transaction read the same rule.
  A gone supervisor's session is recorded `stopping` first and `stopped` as `superseded` only after the sweep.
  A sweep that did not finish leaves the hold on, and `director stop` again sweeps a `stopped` session that is still `Sweeping` rather than refusing it; once it is finished, a Guildmaster's `task start` with no Director running is admitted again.
  The hold reads only the newest session, so `director start` refuses, recording nothing, while that session is `Sweeping` or `stopping` with its supervisor gone (`director: stop sweeping`), naming `director stop` to finish it; a new session would otherwise hide the unfinished sweep, and `director stop` again would target the new session.
  `Engine.Hold` stays as an early read that spares a held advance its preparation; it no longer decides.
  A replayed start returns its receipt whatever the session is now, so replay identity is unchanged.
  Another uncoordinated read before the start was rejected: it only narrows the window.
- **Stop records an unowned attempt stopped under its task's lock.**
  An advance holds the task's lock from recording an attempt until its launcher owns it, or it has handed the attempt to the Director, and `step finish` holds it to publish a stage's result.
  `director stop` takes that lock for an attempt with no unreleased owner, so no advance or finish is part way through it, and records it with `store.StopUnownedAttempt`, which refuses an attempt a launcher owns (`store.ErrOwned`) in the same transaction; a launcher's later `TakeOwnership` is refused because the attempt has finished.
  The stopped stage releases its running capacity and is retried like any stopped attempt.
  `launch.Stop` is unchanged: an ownerless attempt is not taken as finished in general, only by this sweep under the lock.
- **A published stage result is not stopped.**
  A stage whose result `step finish` already published has finished its work, as a stop after an Artisan's result is no stop; its advance takes the result.
- **The runtime runs in a containment unit.**
  The supervisor starts the runtime in `artisan.NewSessionUnit`, the Artisan's containment backend, and ends the unit rather than one process: a termination to the whole unit on Unix, the Job Object killed on Windows.
  However the runtime ends, the supervisor kills what is left in the unit and returns only once it is empty, so a replacement session never overlaps the old one's tools.
  On Windows the session unit keeps the runtime in the supervisor's console process group, so the console's Ctrl+C still reaches it; its interrupt rung, which would reach the supervisor too, is refused.
  The launchers of the attempts the runtime starts are not the session's: they keep their own ownership and stop, so they leave the unit when `launch.Process` starts them.
  A process-group unit loses them to their own session already; in a cgroup unit the supervisor names its own cgroup in the runtime's environment (`GUILDHALL_DETACH_CGROUP`) and the launcher is created there; a Windows session job allows breakaway and the launcher asks for it, which no other descendant does.
  Where that is refused, such as a cgroup that cannot be entered or a job chain above the session's that forbids breakaway, the launcher starts in the unit as before and ends with the session.
  On the subreaper backend a launcher orphaned by its advance reparents to the supervisor, which does not reap it, so an exited launcher stays a zombie until the supervisor exits.
  The unit's limits are the Artisan's: on a process-group backend a descendant that calls `setsid` leaves it.
  Linux's parent-death signal now also ends the runtime when its supervisor dies.

## Not folded in

The launcher stop-reply race (a launcher exiting before its endpoint writes the stop reply) is inside the Artisan launcher's endpoint, not this boundary, and is left to its own change.
