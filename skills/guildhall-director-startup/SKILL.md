---
name: guildhall-director-startup
description: Use as the Director when a session starts and its first message names startup.md, directly or as /version startup "<path>", to read your notes, work every in-flight item with the command it names, then run guildhall director ready.
---

# Director startup

A new Director session starts `recovering`: it starts no new work until you say it is ready.
The first message names the numbered session's `startup.md`; use that exact path, which may be outside your stable runtime working directory.
In Claude Code the first message is `/version startup "<path>"`: reply first with only the version output, as that command says, then read `startup.md` at that path.
It holds your live notes under `## Notes`, if any, and the in-flight snapshot, which is the `needs_me` list of `guildhall director status --json` with the command for each item.
The binary refuses `guildhall task new`, and `guildhall task start` of a task with no attempt yet, until you run `guildhall director ready`, so recover first and start new work after.

## Steps

1. Read `startup.md` at the exact path in the first message.
2. Read the `## Notes` section, if there is one: what earlier sessions promised the Guildmaster, what the Guildmaster told them and what they planned, grouped by kind, one line each, such as `- n7 guildhall/d20 (s16, 2d): <text>`.
   Notes do not repeat what the store holds, so read the store for that.
   - Act on or remove each note flagged `[task done, check it]`: its task has closed, so keep it only if something is still owed, with `guildhall director note edit <id> <text>`, and otherwise run `guildhall director note remove <id> --reason <text>`.
   - When the section opens with `Notes total <N> KB, over 8 KB`, compact the notes: merge, shorten or remove them with `guildhall director note edit` and `guildhall director note remove`.
3. Run `guildhall director status --json` for the current snapshot, since `startup.md` is a copy taken when the session started.
4. Work the `needs_me` items in order, one at a time, with the command each names and the skill below that covers it:
   - `recovering`: this session; it is the last thing you do, in step 6.
   - `advance`: a finished attempt whose launcher could not hand it on; run the `guildhall step done` or `guildhall task start` the item names.
   - `retry`: a failed or stopped attempt; decide whether to retry it with `guildhall-director-tasks`.
   - `resume`: a running attempt whose launcher is stuck, exited, unknown, or settled on disk but not recorded; run `guildhall artisan resume` with the flags the item names.
   - `stalled`, `decision`, `messages` and `director`: handle them with `guildhall-director-inbox`.
   - `paused`: a task held while the session was paused; once you are ready and not paused, start it again with `guildhall task start` as the item names.
   - `checks`: not yours; your session's supervisor reads the task's checks again on its own and wakes you when they finish, so never poll them or run its `guildhall task start` yourself.
   - `maintenance`: a note audit or a hallmark is due; it does not hold back `guildhall director ready`, so work it with `guildhall-maintenance` after recovery.
   - `install`: the installed `guildhall` is behind main; it waits on the Guildmaster, not you.
     When it appears or its count changes, tell the Guildmaster how many merges behind it is and which pull requests it is missing, and that it is time to install.
     If tasks in flight are worth waiting for before installing, name them; that is your judgement.
     Never keep a note about installs: the item stays until the installed binary catches up.

   The `## Running` section in `startup.md`, and `running` in the status, list every attempt in flight with its state and its last heartbeat and output; read it and confirm each attempt is `running` before `guildhall director ready`, as anything that is not healthy is already a `resume` or `stalled` item.
   Parked attempts, listed under `Parked` in `startup.md` and `parked` in the status, are held on purpose with the reason shown; they are information, not work, so leave them parked unless their hold has ended.
5. A `director-stage` message is a grill or other stage that is yours to run; run it with `guildhall-director-stage`; a grill the previous session left open starts again.
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
