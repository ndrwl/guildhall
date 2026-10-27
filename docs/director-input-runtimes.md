# Director input on other runtimes (design note, not built)

The Claude Code Director is woken without touching its input box ([decision](decisions/director-wake-hook.md)).
Keeping the Guildmaster's in-flight input, their half-typed text and their queued messages, across a restart is not built for any runtime, Claude Code included.
A Claude Code restart today loses half-typed text and queued messages, as it did before d8.
This note says what any Director runtime must provide for a wake that leaves the box alone and for restart continuity, and how each degrades when it is missing; it is design input for any later task.
Nothing here is built beyond the Claude Code wake; the fake runtime, which is test-only, keeps typed wakes.

## What a runtime must provide

1. **A wake that does not touch the input box.** A way for a process outside the runtime to start a turn, or add to the running one, with Guildhall's counts-only line, without typing into the box the Guildmaster types in.
   Without it, the only wake is typing, which collides with a person: such a runtime is not woken, its wakes are recorded `queued` and the Guildmaster sees the wake notice instead.
2. **A way to read and restore a draft.** Read the box's half-typed text before the runtime ends, and put it back, unsent, in the next session's box.
   Without an API, a restart loses the draft; reading the box from the screen was judged too brittle to build on.
3. **A durable record of queued input.** Messages the Guildmaster submitted while the runtime was busy, recorded where they survive the runtime's end, with a mark when each is taken.
   Without it, only a reload or stop can read the queue, from the screen before the runtime ends; a crash loses it.

## Findings

- **pi** can wake through an extension: `pi.sendMessage` with `triggerTurn` starts a turn, and `getEditorText` and `setEditorText` read and restore the draft.
  Its queue lives in memory only, so a crash loses it.
- **Codex** hooks cannot start a turn.
  Its app-server can, with `turn/steer` and `thread/queue/add`, as can `codex queue` and `codex --remote`.
  Its queue is durable, in SQLite.

## Long-term target

A program-driven Director: a structured input channel the runtime reads, separate from the human UI the Guildmaster types in, with Guildhall journalling what it sends.
Waking would then never share a box with a person, and in-flight input would be the runtime's own durable record.
This design and restart continuity are not part of d8 and are not queued yet.
