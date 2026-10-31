---
name: guildhall-steward-handoff
description: Use as the Steward when a wake line says handoff due, your context fills, or the Guildmaster asks you to hand off, to keep your notes current and then run guildhall steward handoff.
---

# Steward handoff

A long Steward session fills its context, so the Steward hands off: Guildhall starts a fresh session whose `startup.md` lists every live note and in-flight rollout approval.

## When

- A wake line ends `; handoff due (<reason>)`.
- Your context fills.
- The Guildmaster asks you to hand off.

Finish the message or step you are on first, and never hand off in the middle of a rollout command.

## Before you hand off

Read your notes:

```sh
guildhall director note list
```

Keep them current with `guildhall director note edit`, `guildhall director note remove` and `guildhall director note add`, which work on your own copy.
A note holds only what the next session cannot re-read: a promise to the Guildmaster (`promised`), context they gave (`guildmaster`), or your plan (`plan`).
Each note is one line of at most 1024 bytes, and sensitive data such as keys, tokens, passwords and personal details never goes in a note.
A rollout approval or receipt is already in the Steward's store, where `guildhall steward status` shows it, and needs no note.

## Hand off

Start the fresh session:

```sh
guildhall steward handoff
```

Then tell the Guildmaster in one sentence that you are handing off and that the next session picks up from the notes.
A reload the Guildmaster asks for, as opposed to a handoff, is `guildhall steward reload`.

The Director's `guildhall-handoff`, with its note audit, maintenance record and Director reload, is not the Steward's.
