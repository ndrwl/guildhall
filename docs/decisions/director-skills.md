---
status: accepted
date: 2026-10-22
topic: director
amended-by: director-reload-and-notes.md
formerly: D75, 0075-director-skills.md
---

# The release ships five Director skills split by moment, and tests hold them to the README's commands

## Context

[Conversation first](director-conversation-first.md) makes plain English to the Director the primary interface and requires the Director's skills to cover everything a Guildmaster would otherwise type.
The release now ships those skills, and a test holds them to that rule.

## Decision

- **Five skills, by when the Director needs them.**
  `guildhall-director-startup` handles a new session's `startup.md` and ends with `director ready`; `guildhall-director-inbox` runs on every wake line and turn start; `guildhall-director-tasks` turns what the Guildmaster says into commands, including answering decisions in conversation and merging; `guildhall-director-stage` runs a [Director stage](engine-advance.md) and finishes it with `step finish`; `guildhall-handoff` hands over to a new session and reloads ([skills](../director.md#skills)).
- **Coverage is checked mechanically.**
  A table test reads the README's command list and fails for any command no Director skill names as `guildhall <command>`, except a short list the Guildmaster never asks the Director for, each with why: `help`, `store init`, `director start`, `artisan start`, the `fake-artisan` and `claude-artisan` wrappers, `steward rollout` and `render`.
  So a new README command is not complete until a skill can call it.
- **Skills name only real commands.**
  The CLI's help test fails for any command a release skill names that the help does not list; a command not yet built is listed as pending with its slice, and a pending command the help lists fails the test until it leaves the list.
- **`/guildhall-handoff`, not `/handoff`.**
  Claude Code invokes a skill by its name and every release skill carries the `guildhall-` prefix, so the Guildmaster types `/guildhall-handoff`; the same directory serves the Director and the Guildmaster.
- **Nothing goes to a temporary directory.**
  What a handoff carries is recorded in the store, and a stage's drafts live under the session directory, so a fresh session can trust both.
- **Credits exactly where adapted.**
  Only `guildhall-handoff` is adapted, from mattpocock/skills `productivity/handoff`, so only it carries `CREDITS.md`; a test checks that a skill carries the notice exactly when its body names an upstream source.

## Alternatives rejected

- One skill per command: splitting by moment keeps each description precise enough for the runtime to load the right one.
- The upstream handoff skill's temporary directory: a fresh session could not trust what it finds there.

## Consequences

The handoff skill first recorded a note with `director handoff --note-file -`; since [notes](director-reload-and-notes.md) it runs the note audit and `director reload --now`.
`director pause|resume|stop` landed before the skills, which follow their built behaviour, including that `stop` also stops running attempts.
No live Claude Code Director session had followed these command sequences when this was decided; the [safety checks](director-safety-checks.md) script the startup recovery and task commands over stand-in runtimes.
