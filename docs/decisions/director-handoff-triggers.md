---
status: accepted
date: 2026-10-25
topic: director
formerly: D85, 0085-director-handoff-triggers.md
---

# Three triggers tell the Director a handoff is due, and Claude's context is read from the transcript by the core turn-end hook

## Context

A Director session's context fills up as it works, so the session should hand over before it runs out rather than when the captain notices.
This was the second half of the slice 4 handoff plan, after [reload and notes](director-reload-and-notes.md).

## Decision

- **Three triggers, in `[director]`.**
  `handoff_after_tasks` (off unless set) counts tasks moved to `done` or `cancelled` since the session was recorded; `handoff_context_percent` (70 unless set, 0 off) compares the context the runtime last reported as used; `handoff_idle` (off unless set) is how long the Director may go with no turn signal and no wake ([automatic handoff](../director.md#automatic-handoff)).
  A trigger never reloads the session itself: it asks the Director to check its notes and run `director reload --now`, through the `guildhall-handoff` skill.
- **The trigger rides on a wake line**, so no extra model turn is spent: the line ends `; handoff due (<reason>)`, two reasons joined with `, `, and a `HandoffDue` is recorded per trigger once the line is delivered.
  The idle trigger has no message to ride on, so the supervisor's one timer wakes with the same line.
- **Once per crossing.**
  The `HandoffDue` mutation id names the session, the trigger and the crossing: the task trigger crosses every `handoff_after_tasks` finished tasks, so a Director that carries on is reminded again; the context trigger once per session, since context rarely shrinks within one; the idle trigger once per quiet period.
  A wake that fails to deliver records no crossing, so the next wake carries it; two racing wakes may both carry it.
- **The session record carries the policy**: a wake runs in whichever process stored the message, which may have no config, so the supervisor records the thresholds and the absolute `context.json` path in the `DirectorSession`; a changed threshold takes effect with the next session.
- **The context report is an optional runtime capability (R1.24)** that writes a file, not the store ([`runtimes.Session`](../contracts/adapter-runtimes.md)); the status feed's session shows the newest report.
- **Claude's context comes from the transcript.**
  The core `director turn-ended` hook reads the latest main-session assistant usage from `transcript_path`, input plus cache input tokens against the model's known context window from the published [Anthropic context-window table](https://platform.claude.com/docs/en/build-with-claude/context-windows), checked on 2026-10-24.
  The display-only [Lanterns](lanterns-visuals.md) status line (`context-report --lanterns`) cannot overwrite it, and removing Lanterns does not remove the trigger.
  The unflagged `context-report` status-line reporter is kept for older generated workspaces.

## Alternatives rejected

- A trigger that reloads by itself: only the Director knows what to write in its notes.
- A separate reminder turn: the wake line already reaches the Director.
- Counting or guessing a percent from billing totals, cumulative tokens or unknown, provider-specific model ids: they invalidate the prior report instead.

## Consequences

The known-capacity table needs upkeep as model families change.
Reads are bounded; a failed or malformed transcript never suppresses the turn-end signal, and a transient read failure leaves the prior report.
Cost and token totals are not recorded here; spend stays with the attempt meter.

## History

- 2026-10-23, D85 (0085-director-handoff-triggers.md): set the three triggers, the wake suffix, the crossing ids and the recorded policy.
  It gave the trigger no power to reload because only the Director could write the handoff note and only a note kept an open grill (the never-mid-grill rule); [notes](director-reload-and-notes.md) later replaced the note, and the trigger now asks for a note check and `reload --now`.
  The context percent came from Claude's status-line input through `director context-report`.
- 2026-10-25, from lanterns-and-core-context.md (now [Lanterns visuals](lanterns-visuals.md)): the new workspace's context input moved off the status line into the core `turn-ended` hook, so the status line could become Lanterns' display; the thresholds, crossing ids and wake behaviour stood.
