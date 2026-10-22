---
name: guildhall-handoff
description: Use as the Director to hand off to a fresh session, when the Guildmaster types /guildhall-handoff or asks for it, or a wake line says handoff due, by recording a handoff note and reloading.
---

Adapted from `handoff` in mattpocock/skills (MIT, commit 5beb35b); see CREDITS.md.

# Handoff

A long session fills its context, so the Director hands off: you compact this session into a note, record it, and Guildhall starts a fresh session whose `startup.md` names the note.
The Guildmaster can ask for it by typing `/guildhall-handoff`, optionally followed by what the next session should focus on.

## When

- The Guildmaster types `/guildhall-handoff` or asks you to hand off.
- A wake line ends `; handoff due (<reason>)`.

Never hand off in the middle of a command: finish the message or step you are on first.
In the middle of a grill, do not finish the stage; carry its open questions in the note instead.

## Before you write

Record everything the next session must not lose: a reply the Guildmaster gave that is not yet a `guildhall decide answer`, a task they asked for that is not yet a `guildhall task new`.
The store is the record, so the note never has to carry what a command can record.

## The note

Write the note for a fresh Director that knows your skills but nothing of this conversation.
It does not repeat what the store holds, such as tasks, decisions, attempts, messages and documents: name them by id, and the next session reads them with `guildhall director status --json`.

```markdown
# Handoff from Director session <n>

## Doing
- <what you were in the middle of, by task and decision id>.

## Promised
- <what you told the Guildmaster you would do or tell them, in their words where it matters>.

## Open grill questions
- <repo>/<task> <step> attempt <n>, draft at <path under the session directory>: <each question still open, numbered as you asked it, with your recommended answer and any partial answer>.

## The Guildmaster
- <preferences and context they gave in conversation that no record holds>.

## Suggested skills
- <the skills the next session should run first, such as guildhall-director-stage for the open grill>.
```

Leave out a section with nothing in it.
When the Guildmaster said what the next session is for, lead with it and tailor the note to it.
Redact anything sensitive: keys, tokens, passwords and personal details never go in a note.

## Recording it

Record the note from standard input; never save it to a temporary directory, where a fresh session cannot trust it:

```sh
guildhall director handoff --note-file - <<'NOTE'
# Handoff from Director session 3
...
NOTE
```

Guildhall stores it as a document of the copy and names it in the next session's `startup.md`.
Then start the fresh session at the end of this turn:

```sh
guildhall director reload --now
```

`guildhall director reload --now` is refused unless this session recorded the newest note, and, during a grill, unless the note was recorded after the grill's attempt started.
Tell the Guildmaster in one sentence that you are handing off and that the next session picks up from the note.
