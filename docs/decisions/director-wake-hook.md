# Waking a Claude Director without typing into its input box

This decision supersedes the typing parts of [D69](0069-director-wake.md) for every runtime but the fake: Guildhall never types a wake into a Claude Code Director's pane, whatever its input box holds.
D69's store-first wake, its one recorded decision per message (`wake:<id>`), its counts-only line, its ten-second bound and its rule that no store lock is held while waking all stand.

## Why typing had to go

D69 woke the Director with `herdr pane run <pane> <line>`: the line and Enter, typed into the pane.
The pane's input box is the Guildmaster's too, so a wake that landed while they were half-way through a sentence sent their half sentence with Guildhall's line glued on, or typed Guildhall's line into the middle of theirs.
Guildhall cannot read the box before typing without a race, and any typing at all is a collision with a person, so the fix is a channel that does not touch the box.

## The waiter

Claude Code runs a command hook marked `"async": true, "asyncRewake": true` in the background; when it exits 2, its standard error wakes the session as a new turn, or, when a turn is running, is added to that turn.
The [Phase 0 probes](../evidence/director-input-2026-10-27.txt) showed, on Claude Code 2.1.312, that such a hook wakes an idle session, wakes it with half-typed text in the box and leaves that text untouched and unsent, and still works with a 24-hour `timeout` well past the 600-second default.

The Director session's settings therefore arm `guildhall director wait-wake`, with `async`, `asyncRewake` and `timeout` 86400 seconds, on `SessionStart`, `UserPromptSubmit` and `Stop`, so an idle Director always has one armed; `StopFailure` keeps only `turn-ended`.
The waiter registers with the session supervisor over the session endpoint (`supervisor.sock`) with a new `wait-wake` operation and long-polls it: each poll is held for up to 30 seconds and answered with a wake line, with nothing, so the waiter polls again, or as retired.

- One waiter per session: a registration under a new id retires the older waiter, whose poll is answered retired and which exits 0 silently.
- Handed a wake, the waiter prints the line, Guildhall's own `HandoffLine(WakeLine(...))`, on standard error and exits 2.
- With no supervisor listening, such as while one restarts, it polls again every second instead of exiting; it exits 0 once its session is recorded stopped or a newer session exists.
- Five minutes before its timeout a waiter still registered tells the supervisor with a `refresh` operation and exits 2 with a counts-only line, so the short turn that follows arms a new waiter through its `Stop` hook.

A waiter counts as registered while a poll of it is open and for five seconds after one ends, so the supervisor can tell a live waiter from one that ended, which only happens when it is killed.

## Wake delivery

`messaging.Wake`, `WakeResumed` and `WakeIdle` deliver through one seam.
The session records the runtime adapter it runs (`store.DirectorSession.Adapter`) and its directory (`Dir`); only the fake runtime, which is test-only and has no hooks, is typed into, and every other session, a session recorded with no adapter among them, is handed the line with a `wake` operation on its endpoint.
A Claude session can never fall back to typing.
A wake the endpoint delivers is recorded `woke` exactly as a typed one was; one with no waiter registered, or no supervisor listening, is recorded `queued` with the new reason `no waiter`, and the message waits in the inbox as every queued wake's does.

The supervisor keeps two duties.

- It counts each wake a waiter took, and each refresh, as the Director's activity, counted before any turn signal arriving at the same moment, so the Idle rule, the quiet point of a plain reload and the idle trigger are decided as they were for a typed wake: the Director is idle again once a turn has ended since.
  The probes showed a rewake turn fires `UserPromptSubmit` like a typed prompt, and that a rewake arriving mid-turn joins that turn rather than starting another, so the next turn end ends the wake's activity either way.
- Once a waiter registers after a wake was recorded `no waiter`, it hands that waiter one wake of the current unread counts.

While no waiter is registered and messages are unread, after a missed wake or with the Director idle, the supervisor shows a wake notice outside the input box: the pane's Herdr title, set with `herdr pane report-metadata <pane> --source guildhall --title "guildhall: 2 unread (note 2)"` and cleared with `--clear-title` once the wake is delivered or nothing is unread.

## What it costs

A waiter killed while the Director is idle cannot be replaced by the supervisor, which cannot run a hook: the wake waits, with the notice showing, until the Guildmaster's next prompt or a `Notification` hook re-arms it (spec open question 1, answered "the notice only").
An idle Director takes one short refresh turn a day.
The wake line is still Guildhall's counts and never a sender's words, which matters more now: the probes showed the model is wary of hook text that asks it for something.
