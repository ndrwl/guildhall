# Clerk

Clerk is Guildhall's read-only Claude Code plugin for the Guildmaster.
Its five slash commands answer from the local copy, in the Director's session or in a Claude Code session of your own, without asking the Director:

| Command | What it shows |
|---|---|
| `/guild [--all]` | the open tasks by priority, with where each stands; what needs you; the open pull requests; the merge queue in short. `--all` adds the tasks closed in the last 7 days. |
| `/decisions` | every open decision, with its question and numbered options, and whether it is deferred |
| `/task <id>` | one task's state, priority, kind and title, where it stands, its pull request and its steps |
| `/inspect <id>` | everything `/task` shows, then the timeline (attempts, decision waits, findings, rulings, priority changes, merge holds), attempt summaries, documents and spend |
| `/queue` | each repository's merge queue in full, front first |

Clerk changes nothing: it answers no decision, starts or stops no work, changes no priority, never holds, releases or reorders the merge queue, and writes no note.
Ask the Director for any of those.

Each command runs one `guildhall clerk` subcommand and the model replies with its output verbatim, so an answer costs one small model turn and reads the same every time.
You can run the subcommands yourself in a terminal: `guildhall clerk guild --all`, `guildhall clerk task d34`, and so on ([contract](contracts/clerk.md)).

## Loading it

Clerk ships inside the `guildhall` binary, like [Lanterns](lanterns.md).
Every `guildhall director start` (and so every reload and handoff) writes a fresh copy of it from the running binary into `<copy>/director/claude/.claude/plugins/clerk`, and the Director's own session loads it with a second `--plugin-dir`.
An upgraded binary's Clerk arrives with the next start.

To use it in your own Claude Code session, load the same directory:

```sh
claude --plugin-dir <copy>/director/claude/.claude/plugins/clerk
```

or keep an alias in your shell profile:

```sh
alias guild-claude='claude --plugin-dir "$HOME/guildhall/director/claude/.claude/plugins/clerk"'
```

The directory exists only once the copy's Director has been started with a release that has Clerk; until then, start the Director once (`guildhall director start --copy <copy>`).
Start the session inside the copy (or any directory under it, other than `state/`) so Clerk finds the copy as other `guildhall` commands do; elsewhere it says `no Guildhall copy found from <dir>; pass --copy <dir> or cd into a copy`.
Each command may run only its own `guildhall clerk <name>` subcommand without asking.

## Sample output

```text
Guild, as of 2026-10-30 12:00 UTC, local state

Tasks
TASK  PRI  KIND     TITLE                          WHERE IT STANDS
d31   p0   feature  Rebase-aware review            in merge queue 1/3, rebasing
d35   p1   feature  Clerk                          build running
d40   p2   bug      Fix the crash on reload        CI pending, PR #212
d36   p3   feature  Steward views                  not started

Needs you
- decision d41 accept-plan: Accept the plan?

Pull requests
PR    TASK  CHECKS
#212  d40   CI pending

Merge queue
guildhall: 3 queued, front d31 rebasing
```

When more than one repository has tasks, tasks are named `<repo>/<id>`.
`/task` and `/inspect` take an id alone when only one repository has that task, and `<repo>/<id>` otherwise.

## Where it stands

Each task's position is one plain phrase:

| Source | Phrase |
|---|---|
| no `next.json` | `not started` |
| `wait: attempt` with a running attempt | `<step> running` (for example `build running`) |
| `wait: attempt`, not yet running | `<step> starting` |
| `wait: decision` | `waiting for your approval` (or `waiting for your answer` for a question) |
| `wait: director` / `director-stage` | `waiting for the Director` |
| `wait: checks` | `CI pending, PR #<n>` |
| `wait: merge-queue` | `in merge queue <pos>/<len>`, plus `rebasing`, `CI pending` or `waiting for your answer` when the front waits on one (for example `in merge queue 1/3, rebasing`) |
| `wait: queued` | `queued <pos>` |
| `wait: paused` | `paused` |
| `wait: dependencies` | `waiting on <task ids>` |
| `wait: done` / closed | `merged`, `done` or `cancelled` |
| any other value | `waiting (<raw value>)` |

## Limits

- **Local state only.** Every answer says `local state`: Clerk makes no forge or network call.
  A pull request's checks are what the store already holds: `CI pending` while the task waits on them, else the newest checks message (`CI passed` or `CI failed`), else `no checks recorded`.
- **This copy only.** Other machines, other Directors and the Steward are not shown.
- **A busy store.** If the Director is writing for longer than the store wait, Clerk says `the store is busy (the Director is writing); try again in a moment` rather than an empty answer.
- **No marker writes.** `--copy` must name a copy that already has its marker; Clerk never marks one.
