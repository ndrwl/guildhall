---
name: guildhall-handoff
description: Use as the Director to hand off to a fresh session, when the Guildmaster types /guildhall-handoff or asks for it, or a wake line says handoff due, by running and recording the note audit and reloading.
---

Adapted from `handoff` in mattpocock/skills (MIT, commit 5beb35b); see CREDITS.md.

# Handoff

A long session fills its context, so the Director hands off: Guildhall starts a fresh session whose `startup.md` lists every live note.
The Guildmaster can ask for it by typing `/guildhall-handoff`, optionally followed by what the next session should focus on.

## When

- The Guildmaster types `/guildhall-handoff` or asks you to hand off.
- A wake line ends `; handoff due (<reason>)`.

Never hand off in the middle of a command: finish the message or step you are on first.
A grill in progress need not be finished or noted: the next session restarts it.

## Before you reload

Record everything the next session must not lose: a reply the Guildmaster gave that is not yet a `guildhall decide answer`, including a reply to your own question about a task (open it with `guildhall decide open` first if it is not yet recorded), a task they asked for that is not yet a `guildhall task new`, requirements they gave about one task that are not yet its `guildhall task context add`, another task's document they wanted a task's Artisans to see, or a document a task's statement depends on, that is not yet its `guildhall task link add`, a setting they asked one task to override that is not yet its `guildhall task override set`.
The store is the record, so a note never carries what a command can record.

Then run the note audit from `guildhall-maintenance`, whether or not it is due, and record it with `guildhall director maintenance record --notes --summary <text>`.
You keep notes as you go (`guildhall-director-inbox`), so this is a check, not a write-up:

```sh
guildhall director note list
```

- Edit a note that has changed: `guildhall director note edit <id> <text>`.
- Remove one that is done, no longer true or already held by the store: `guildhall director note remove <id> --reason <text>`, naming the record that holds it.
- Bring any recurring kind of note that should become procedure or a task to the Guildmaster as a short list, and create nothing from it before they approve.
- Add only what no record holds and the next session cannot restart or re-read: a promise to the Guildmaster (`promised`), a preference or context they gave that is not about one task (`guildmaster`; requirements about one task go in its task context), or your intent (`plan`), with `guildhall director note add --kind <kind> [--task <repo>/<id>] <text>`.
- When the Guildmaster said what the next session is for, add it as a `plan` note.
- A standing rule for one repository is not a note: propose it for that repository's `.guildhall/policies.md` as a task, or tell the Guildmaster the exact line to add, and keep `guildmaster` notes for context that is not a policy.

Each note is one line of at most 1024 bytes; redact anything sensitive: keys, tokens, passwords and personal details never go in a note.

## Reloading

Start the fresh session at the end of this turn:

```sh
guildhall director reload --now
```

Tell the Guildmaster in one sentence that you are handing off and that the next session picks up from the notes.
