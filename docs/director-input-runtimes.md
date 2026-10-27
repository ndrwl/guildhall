# Director input on other runtimes (design note, not built)

The Claude Code Director is woken without touching its input box ([decision](decisions/director-wake-hook.md)), and a restart is to keep the Guildmaster's in-flight input: their half-typed text and their queued messages.
This note says what any other Director runtime must provide for the same guarantees, and how each guarantee degrades when it is missing.
Nothing here is built; the fake runtime, which is test-only, keeps typed wakes.

## What a runtime must provide

1. **A wake that does not touch the input box.** A way for a process outside the runtime to start a turn, or add to the running one, with Guildhall's counts-only line, without typing into the box the Guildmaster types in.
   Without it, the only wake is typing, which collides with a person: such a runtime is not woken, its wakes are recorded `queued` and the Guildmaster sees the wake notice instead.
2. **A way to read and restore a draft.** Read the box's half-typed text before the runtime ends, and put it back, unsent, in the next session's box.
   Without an API, a screen parse of the box is the fallback, as for Claude Code; without either, a restart loses the draft.
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
This design is not part of d8 and is not queued yet.
