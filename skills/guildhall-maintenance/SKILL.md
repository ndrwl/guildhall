---
name: guildhall-maintenance
description: Use as the Director when guildhall director status lists a maintenance item, and at every handoff, to audit your notes against the store and record the audit with guildhall director maintenance record, and to launch a due hallmark.
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

For each repository with a `hallmark due` item, launch a hallmark: a `hallmark` task of that repository, created with `--kind hallmark` and started (`guildhall-director-tasks`).
There is nothing to record: Guildhall reads the hallmark from its task.

```sh
guildhall task new --repo <repo> --kind hallmark --title "Hallmark after 4 merges" <id>
guildhall task start --repo <repo> <id>
```

While the task is open the item stays out of `needs_me` and status shows the task as the repository's `running_task`, so never launch a second one.
The task stays open until its `follow-ups` decision is answered, deferred or not, so the next hallmark waits for the Guildmaster's feedback on this one.
Once it closes it is the repository's last hallmark, and the count of merges toward the next one starts from the `follow-ups` answer, so merges made before the answer do not count.
A hallmark that fails, and that the Guildmaster agrees to cancel, never resets the count; the item comes back, and you launch a new one.

The task runs one read-only stage that uses `guildhall-hallmark` and is given the repository's previous hallmark report; Guildhall fails the stage if it changes the project.
Its test evidence needs the repository's `[repository.profiles.researcher]` grant for the test commands; without one the report lists that evidence as missing.
When its report is in, Guildhall opens its `follow-ups` decision and sends you a `finished` message naming the report, with no pull request; the task stays open, waiting on the decision.
Read the report, then:

1. Present the `follow-ups` decision to the Guildmaster: `guildhall decide show` gives the report path and its ranked `H<n>` titles.
2. Answer it with `guildhall decide answer`, in the Guildmaster's words: `pick` with the ids they chose on the following lines, such as `H1, H3`, or `none`.
3. Run `guildhall task start --decision follow-ups --repo <repo> <id>`; answering never advances a task, and this advance closes the hallmark done.
4. Run `guildhall task new` for each picked id only; Guildhall creates no task from the answer.

Then read the self-review ledger, as `guildhall-self-review` asks at hallmark time, and give its lowest criteria to the hallmark's findings.
A hallmark the Guildmaster asks for out of turn is launched the same way.

## Checking

A note audit item leaves `needs_me` once the run is recorded, and a hallmark item once its task is created.
`guildhall director status --json` shows in `maintenance` the last note audit, sessions until the next, and, per repository, the last hallmark, merges until the next and the `running_task` while a hallmark runs.
`guildhall director maintenance list` shows every recorded run with its summary, hallmark runs recorded before hallmarks were read from their tasks among them.

## Rules

- A summary is one line of counts, ids and short phrases; never keys, tokens, passwords or personal details.
- A note whose proposal waits for the Guildmaster stays until they answer.
- A hallmark never changes the project: it reports findings, and the Guildmaster chooses which become tasks.
