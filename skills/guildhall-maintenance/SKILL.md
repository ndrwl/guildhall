---
name: guildhall-maintenance
description: Use as the Director when guildhall director status lists a maintenance item, and at every handoff, to audit your notes against the store, launch a due hallmark, and record each run with guildhall director maintenance record.
---

# Director maintenance

Maintenance keeps the Director's own records lean and the codebase reviewed, without relying on you to remember.
It has two parts: the note audit and the hallmark.
Guildhall counts sessions and merged pull requests and lists a `maintenance` item in `needs_me` when a part is due; it is not urgent, so it never wakes you and never holds back `guildhall director ready`.
Work it after recovery, when nothing more pressing is in `needs_me`.

## When

- `guildhall director status --json` lists a `maintenance` item: `note audit due` or `hallmark due` for one repository.
- You hand off (`guildhall-handoff`): run the note audit then, whether or not it is due.
- Never while the session is `recovering`: a hallmark needs `guildhall task new`, which recovery refuses.

## The note audit

List every live note:

```sh
guildhall director note list
```

Ask three questions of each note, in this order:

1. Is it still true?
   If it is not, remove it: `guildhall director note remove <id> --reason <text>`.
2. Does the store already hold it, as a task, a decision, a message, a task's context, a document or the config?
   If it does, remove the note with a reason that names that record, such as `--reason "held by guildhall/d47"` or `--reason "in [director] config"`.
3. Is it a recurring kind of note, one that keeps being written because nothing records it, that should become standard procedure (a skill or role change) or a follow-up task?
   If it is, note the proposal for the list below; leave the note until the Guildmaster has answered.

Remove, edit and compact notes on your own: merge notes that say one thing, shorten long ones with `guildhall director note edit <id> <text>`, and keep what no record holds.
Notes are never removed for you, so every removal is yours and carries a reason.

Bring every proposed task and procedure change to the Guildmaster in the conversation as one short numbered list, one line each, saying which notes it replaces.
Create, launch or change nothing from that list until the Guildmaster approves it; then create what they approved, and remove the notes it replaces with a reason naming it.

Record the run in one line of at most 1024 bytes saying what was removed and what was proposed:

```sh
guildhall director maintenance record --notes --summary "removed 4 (held by d47, D94, config); proposed 2: d58, skill change"
```

## The hallmark

For each repository with a `hallmark due` item, launch a hallmark: a task of that repository whose stage uses `guildhall-hallmark`, created and started as earlier hallmarks were, with `guildhall task new` and `guildhall task start` (`guildhall-director-tasks`).
Record the run with the task's id as soon as it is launched, so merges made while it runs count toward the next one:

```sh
guildhall director maintenance record --hallmark --repo <repo> --task <id> --summary "hallmark launched after 4 merges"
```

Then read the self-review ledger, as `guildhall-self-review` asks at hallmark time, and give its lowest criteria to the hallmark's findings.
A hallmark the Guildmaster asks for out of turn is launched and recorded the same way.

## Checking

The item leaves `needs_me` once the run is recorded.
`guildhall director status --json` shows in `maintenance` the last note audit, sessions until the next, and, per repository, the last hallmark and merges until the next.
`guildhall director maintenance list` shows every recorded run with its summary.

## Rules

- A summary is one line of counts, ids and short phrases; never keys, tokens, passwords or personal details.
- A note whose proposal waits for the Guildmaster stays until they answer.
- A hallmark never changes the project: it reports findings, and the Guildmaster chooses which become tasks.
