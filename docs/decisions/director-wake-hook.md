---
status: accepted
date: 2026-10-28
topic: director
amends: director-safety-checks.md
formerly: D69, 0069-director-wake.md, D77, 0077-wake-outside-the-task-lock.md
---

# One store-first wake tells the Director of unread messages, delivered to Claude through a rewake hook and never typed into its input

## Context

A stored message is not proof that anyone has seen it, and the Director must hear about one while its session runs, without a service (R1.29).
Typing a wake into the Director's pane collided with the Guildmaster, whose input box it is too.

## Decision

- **One wake after every send to the Director.**
  `messaging.Wake` is called after the engine's outbox, `messages send ... director` and the launcher's sends ([launcher stall](stall-observation.md)); `WakeResumed` and `WakeIdle` deliver through the same seam ([waking the Director](../messaging.md#waking-the-director)).
  Only a `running` or `recovering` [session](director-start.md) is woken; every other case records `queued` with its reason, and a paused session is not woken, since pausing is how the Guildmaster quiets the Director.
- **Counts, never text.**
  The line is `guildhall: <n> unread (<type> <count>, ...)`, naming only Guildhall's own types and counting the rest as `other`, so no sender can put instructions in front of the Director.
- **One recorded decision per message**, `wake:<message id>`, recorded after delivery: a replay types nothing new, two concurrent wakes may both deliver and the first record stands, and a failed wake never fails the send.
- **No lock held while waking, and a bound.**
  The wake takes the store through a narrow interface and holds no store lock; an advance wakes for its messages after releasing the task lock; every `herdr` run sets `WaitDelay` to five seconds, so a wake keeps its ten-second bound.
- **Claude is woken by a rewake hook.**
  The session's settings arm `guildhall director wait-wake`, an `async`, `asyncRewake` command hook with a 24-hour timeout, on `SessionStart`, `UserPromptSubmit`, `Stop` and `Notification`; it long-polls the supervisor's `wait-wake` operation on `supervisor.sock` and, handed a line, prints it on standard error and exits 2, which wakes the session or joins the running turn.
  One waiter per session, the newer retiring the older; a waiter refreshes five minutes before its timeout ([the end of a turn](../director.md#the-end-of-a-turn)).
- **Only the fake runtime is typed into.**
  The session records its adapter; every other session, including one with no adapter recorded, gets the line over its endpoint, and a Claude session never falls back to typing.
  With no waiter the wake is `queued` as `no waiter`, the supervisor shows a notice in the pane's Herdr title, and the next waiter gets one catch-up wake.
- The supervisor counts each delivered wake and refresh as the Director's activity, so the idle rule, a plain reload's quiet point and the idle trigger work as for a typed wake.

## Alternatives rejected

- Reading the input box before typing: racy, and any typing collides with a person.
- Recording `woke` before delivering: a recorded wake with nothing delivered would be a lie, while a delivered line that is not seen costs nothing.
- Waking inside the advance: one slow pane held the task lock for 34 seconds.

## Consequences

A waiter killed while the Director is idle cannot be replaced by the supervisor, which runs no hook: the wake waits, with the notice showing, until the next prompt or a `Notification` hook re-arms it.
An idle Director takes one short refresh turn a day.
The [safety checks](director-safety-checks.md) now assert that nothing is typed into a Claude session's pane; other runtimes need a [design](../director-input-runtimes.md).

## History

- 2026-10-22, D69 (0069-director-wake.md): the store-first, counts-only wake, typed into the pane with `herdr pane run`.
  `herdr.Client.RecordedTab` adopted the recorded pane in a later process after checking it still exists (R1.54), and typing refused a pane whose shell was idle, recording `runtime not running`.
  Sender types were `guildhall` and `local`, since `messages send` cannot tell the Guildmaster from an Artisan; the planned `guildmaster` sender of [Director messaging](director-messaging.md) refines that, and the recorded type is the input a per-sender budget will read.
- 2026-10-22, D77 (0077-wake-outside-the-task-lock.md): sustainability review 11 (F36) found the ten-second bound broken, since a killed `herdr` whose child held its pipe kept the call open, and the wake run under the task lock, so a two-message advance took 34 seconds under it, long enough for another advance to lose the lock (F35).
  It added `WaitDelay`, as `forge` and `git` had, and moved the advance's wakes after the lock, one per message in send order, whether or not the advance succeeded.
- 2026-10-28, director-wake-hook.md: typing glued the wake onto the Guildmaster's half-typed sentences, so delivery moved to the rewake hook for every runtime but the fake.
  [Phase 0 probes](../evidence/director-input-2026-10-27.txt) on Claude Code 2.1.312 showed the hook wakes an idle session, leaves half-typed text untouched and unsent, and works with a 24-hour timeout; that a rewake fires `UserPromptSubmit`; and that the model is wary of hook text asking for something, so the line stays counts only.
  A waiter counts as registered while a poll is open and for five seconds after, and no longer once it has taken a line, so a second wake reaches the next waiter as its catch-up rather than a waiter that is gone.
  An idle Director with a killed waiter gets only the notice (spec open question 1, answered "the notice only").
