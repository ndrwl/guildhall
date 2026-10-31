---
status: accepted
date: 2026-10-23
topic: director
amended-by: director-reload-and-notes.md
formerly: D73, 0073-director-pause-stop.md, D84, 0084-director-stop-admission.md
---

# Pause holds new attempts where they are recorded, and stop holds them until every attempt is proven stopped

## Context

The Guildmaster and firstmate need controls over a running Director session, and the captain fixed their meaning: pause stops new work only and running attempts finish; stop also stops the Director's Artisan attempts.
A cross-review then found that an advance read the hold before recording its attempt, that `director stop` reported an attempt with no owner stopped while it stayed unfinished, and that a hold read from the session's state alone was lifted before stop's sweep had finished.

## Decision

- `director pause`, `director resume` and `director stop` move the newest [session record](director-start.md) and print it ([pause and resume](../director.md#pause-and-resume), [stop](../director.md#stop)).
  `director resume` is the Director's lifecycle verb, not `artisan resume`, which settles one attempt whose launcher is gone; they share the verb because the role grammar gives every role the same four.
- A `paused` or `stopping` session starts no new attempt; running attempts, results, decisions and the Guildmaster's answers are unaffected, so `step done` still takes a result and `task start --decision` still feeds an answer.
- The hold is read where the attempt is recorded: `StartCappedAttempt` refuses with `store.ErrDirectorHeld`, writing nothing, in the same transaction as the cap count and the start, so a start either commits before a pause or stop and is seen by stop's sweep, or is refused.
  The engine treats the refusal as `Engine.Hold`, and a held advance writes `{"wait":"paused","step":"<step>"}` to `next.json`, which `needs_me` lists as `paused`, so a task asked for during a pause is still listed after the resume ([runtime record](director-runtime-record.md)).
- `director stop` records `stopping`, sets the session's durable `Sweeping`, stops every running attempt through its launcher, and clears `Sweeping` only once every attempt is proven stopped; the hold is `paused`, `stopping` or `Sweeping`, one rule for `held` and the start transaction.
  `director start` refuses while the newest session is `Sweeping`, or `stopping` with its supervisor gone (`director: stop sweeping`), because a new session would hide the unfinished sweep.
- An attempt with no unreleased owner, such as a Director stage or an attempt recorded but not launched, is recorded stopped by `store.StopUnownedAttempt` under its task's lock, which an advance holds until its launcher owns the attempt; it refuses an owned attempt, and a stage whose result `step finish` already published is not stopped.
- The supervisor ends the runtime once it reads `stopping`: after a grace period that stands in for the end of the turn, or at its next read for `stop --now`; the turn-end rule is [handoff and reload](director-reload-and-notes.md)'s.
  The runtime runs in a [recorded containment unit](director-runtime-record.md), so the stop ends its tools too.

## Alternatives rejected

- Another hold read before the start transaction: it only narrows the window.
- Taking an ownerless attempt as finished in `launch.Stop` generally: only this sweep, under the task's lock, may do so.
- Lifting the hold when the session reads `stopped`: `stop --now` and a gone supervisor record that before the sweep ends.

## Consequences

`Engine.Hold` remains only as an early read that spares a held advance its preparation; it no longer decides.
A sweep that does not finish leaves the hold on until `director stop` is run again, which also sweeps a `stopped` session still `Sweeping`.
A stopped stage releases its running capacity and is retried like any stopped attempt.

## History

- 2026-10-22, D73 (0073-director-pause-stop.md): added pause, resume and stop with the captain's semantics; pause and `stopping` held launches through `Engine.Hold`, stop swept running attempts through their launchers, `stop --now` set `stop_now`, and a plain stop's 60-second grace stood in for the end of the turn until the handoff work brought turn signals.
  A gone supervisor's session was recorded honestly and `pause` and `resume` were refused for it, naming `director stop`; `task start` and `step retry` then read the hold before advancing, which [the runtime record](director-runtime-record.md) later removed so a held start leaves its wait.
- 2026-10-22, D84 (0084-director-stop-admission.md): after the cross-review, moved the hold into the start transaction, added `Sweeping` so a stop holds admission until its sweep ends and a gone supervisor's session goes `stopping` before `stopped` as `superseded`, and stopped unowned attempts under the task's lock; a replayed start still returns its receipt whatever the session is now.
  Its containment of the runtime is now stated in [the runtime record](director-runtime-record.md); the launcher stop-reply race was left to its own change.
