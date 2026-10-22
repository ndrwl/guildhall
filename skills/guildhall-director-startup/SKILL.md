---
name: guildhall-director-startup
description: Use as the Director when a session starts and its first message names startup.md, to read the handoff note, work every in-flight item with the command it names, then run guildhall director ready.
---

# Director startup

A new Director session starts `recovering`: it starts no new work until you say it is ready.
The first message names `startup.md` in your session directory.
It holds the handoff note your previous session left, if any, and the in-flight snapshot, which is the `needs_me` list of `guildhall director status --json` with the command for each item.
The binary refuses `guildhall task new`, and `guildhall task start` of a task with no attempt yet, until you run `guildhall director ready`, so recover first and start new work after.

## Steps

1. Read `startup.md`.
2. Read the handoff note it names, if there is one.
   The note tells you what the previous session was doing, which skills it suggests and any grill questions it left open; it does not repeat what the store holds, so read the store for that.
3. Run `guildhall director status --json` for the current snapshot, since `startup.md` is a copy taken when the session started.
4. Work the `needs_me` items in order, one at a time, with the command each names and the skill below that covers it:
   - `recovering`: this session; it is the last thing you do, in step 6.
   - `advance`: a finished attempt whose launcher could not hand it on; run the `guildhall step done` or `guildhall task start` the item names.
   - `retry`: a failed or stopped attempt; decide whether to retry it with `guildhall-director-tasks`.
   - `resume`: a running attempt whose launcher is stuck, exited or unknown; run `guildhall artisan resume` with the flags the item names.
   - `stalled`, `decision`, `messages` and `director`: handle them with `guildhall-director-inbox`.
   - `paused`: a task held while the session was paused; once you are ready and not paused, start it again with `guildhall task start` as the item names.
5. A `director-stage` message is a grill or other stage that is yours to run; run it with `guildhall-director-stage`, and carry on any open grill questions from the handoff note rather than asking them again.
6. When nothing left in `needs_me` is yours to act on now, run `guildhall director ready`.
   Items waiting on the Guildmaster, such as open decisions, do not hold you back: tell the Guildmaster about them and get ready.

```sh
guildhall director status --json
guildhall director ready
```

`guildhall director ready` prints the session as one JSON line with state `running`.
Then tell the Guildmaster in a few plain sentences what you recovered, what is still waiting on them and what happens next.

## Rules

- Never start new work while the session is `recovering`, even when the Guildmaster asks: say you are recovering, finish recovery, run `guildhall director ready`, then start it.
- A refusal naming `guildhall director ready` means the session is still recovering; it is not an error to work around.
- Recovery never needs a new task: everything in flight is already in the store.
