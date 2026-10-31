# `internal/clerk` and `guildhall clerk`

## Contract

Clerk is the Guildmaster's read-only Claude Code plugin ([user doc](../clerk.md), [decision](../decisions/clerk-install-and-output.md)).
It has two halves: the embedded plugin in `internal/clerk`, which holds no logic, and the `guildhall clerk` subcommands in `internal/cli/clerk.go`, which make every answer.

### The plugin

`internal/clerk` embeds `plugin/` with `//go:embed all:plugin`.
`Files()` returns independent copies of its assets by slash path relative to the plugin root, skipping `plugin/tests`, as `lanterns.Files()` does.
The plugin is `.claude-plugin/plugin.json` (name `clerk`, version, description) and one command per entry of `Commands`, `commands/{guild,decisions,task,inspect,queue}.md`.
Each command is in the `/version` style: its frontmatter's `allowed-tools` is exactly `Bash(guildhall clerk <name>:*)`, never `guildhall *`; its body runs the one command `guildhall clerk <name> $ARGUMENTS` and replies with only its output, verbatim, with no commentary, or on failure with only the error output.

The Claude adapter's `SessionFiles` writes every `clerk.Files()` entry under `ClerkPluginDir`, `.claude/plugins/clerk`, beside Lanterns' `PluginDir`, so every `director start`, reload and handoff writes a fresh copy from the running binary into `<copy>/director/claude/.claude/plugins/clerk`.
`Session` passes `--plugin-dir <dir>/.claude/plugins/clerk` after the Lanterns `--plugin-dir`.
Nothing else in the Director's session changes, and there is no installer: no `guildhall clerk` subcommand writes a file.

### The subcommands

```text
guildhall clerk guild [--all] [--copy <dir>]
guildhall clerk decisions [--copy <dir>]
guildhall clerk task [--copy <dir>] <task>
guildhall clerk inspect [--copy <dir>] <task>
guildhall clerk queue [--copy <dir>]
```

Flags and the task argument may come in any order.
A wrong flag, a missing or extra argument, an unknown subcommand and `--all` on anything but `guild` are usage errors: exit 2 with the usage.
Every other refusal prints one line, `guildhall: clerk <sub>: <reason>`, and exits 1; stdout that cannot be written is exit 1.

Each answer is plain text laid out with `text/tabwriter` at `clerkWidth`, 100 columns: a title in a table is cut with `…` to fit, and prose such as a needs-you reason, a question or an attempt summary is wrapped with a hanging indent.
The first line is `<Answer>, as of <YYYY-MM-DD HH:MM> UTC, local state`, by `App.Now` (wall time when nil).
An empty section prints one line saying so, such as `No open decisions.`; no section is left out without a word.
When tasks of more than one repository exist, tasks are named `<repo>/<id>`, otherwise by id.

The answers are made from the existing reads, called directly: `readDirectorStatus` (through `readDirectorStatusRecords`, with Clerk's lock wait) for each task's `next.json`, steps and live classification, spend, open decisions with their deferred flag, `needs_me` and `merge_queues`; `versionInfo(..., false)` for `needs_me`'s `install` item, as `director status` adds it; then, in one more read-only transaction, `listTasks` and `taskViews` for priorities and merge-queue entries, each task's documents through `interp.TaskPullRequests` for its pull request and whether it merged, and the receipts for each task's close time (its `set-task-state` receipt, as `director status` builds `closedAt`, else its `cancelled_at`) and its newest `checks-passed`, `checks-failed` or `checks-pending` message.
`inspect` reads the task's history once more for `timeline` and `files`.

- `guild` prints **Tasks**, the open tasks (with `--all`, also those closed in the last seven days) by priority, `p0` first, then id, then repository: task, priority, kind, title and where it stands; **Needs you**, only the Guildmaster's asks as `- <kind>[ <task>][ <step>][ <decision>]: <reason>`: each `needs_me` item of kind `decision` or `install` in the feed's order, then one `- director-stage <task> <step>: the Director runs <step> with you in its session` line for each open task whose `next.json` is `wait: director-stage`, in the Tasks table's order (`Nothing needs you.` when there are none); **Waiting on the Director**, every other `needs_me` item (`advance`, `checks`, `merge-queue` and the rest, and any kind added later) in the feed's order and the same format (`Nothing waits on the Director.` when there are none); `needs_me` itself is unchanged; **Pull requests**, each open task with a recorded pull request and no `-merge` document: number, task and checks state; and **Merge queue**, one line per repository with a non-empty queue, `<repo>: <n> queued, front <task> <what it waits on>[, held]` (`next` instead of `front` when no front stands).
- `decisions` prints every open decision: `<task> <id> (<kind>)[, deferred]`, its question and its options numbered from 1.
- `task <id>` prints the task's id and title, its state, priority, kind, where it stands and pull request (`#<n> <url>`, with its checks state while open), then its steps, each step's latest attempt with its outcome, or `running`, or `no outcome yet (<live state>)`, and `, parked`.
- `queue` prints each repository with a non-empty queue, in `mergeQueues`' order, then its entries front first and then in queue order: position, task, priority, title, pull request number, how long since `ready_at`, `held` when its merge is held, and what it waits on; or `The merge queue is empty.`
- `inspect <id>` prints what `task` prints, then the attempts with start, finish and duration; attempt summaries, from each attempt's `result.json` `summary` when it can be read; decisions with their state, opening, answer and wait; review findings; rulings; priority changes; merge holds; documents; and the task's spend.

**Where it stands** is one fixed phrase: no `next.json` is `not started`; `wait: attempt` is `<step> running` when that attempt has no outcome and classifies `running`, else `<step> starting`; `wait: decision` is `waiting for your answer` for a question and `waiting for your approval` otherwise while the named decision is one of the task's open decisions, and `waiting for the Director` once it is answered (or void) but the task not yet continued; `director` and `director-stage` are `waiting for the Director`; `checks` is `CI pending, PR #<n>` (`CI pending` with no pull request recorded); `merge-queue` is `in merge queue <pos>/<len>`, with `, rebasing`, `, CI pending` or `, waiting for your answer` when the entry's `waiting_on` is `rebase`, `ci` or `decision`; `queued` is `queued <position>`; `paused` is `paused`; `dependencies` is `waiting on <tasks not done>`; `done`, and a closed task, is `merged` (a `-merge` document is recorded), `done` or `cancelled`; any other value is `waiting (<value>)`, and a `next.json` that cannot be read is `waiting (next.json unreadable)`, so an unknown wait never fails the command.

**Kind** is `interp.TaskKindOf` ([task kinds](../decisions/task-kinds.md)) with the repository's config and its bound-workflow resolver, memoised per revision for one Clerk read: `hallmark` when the bound workflow resolves and has a hallmark step, then `bug` by the repository's bug workflow name, else `feature`; a repository the config does not list, or a config that cannot be read, uses a zero repository and so the shipped names. `TestClerkKindRenamedHallmark` pins a hallmark task under a renamed `kinds.hallmark`.

**Checks** are local only: `CI pending` while the task waits on checks, else its newest checks message, `CI passed`, `CI failed` or `CI pending`, else `no checks recorded`.
Clerk makes no forge, `gh` or network call.

**Task ids**: `<repo>/<id>` names one task; an `<id>` alone is accepted when exactly one repository has it.
An unknown task is `no task <arg> in this copy`; an id in several repositories is `task <id> is in more than one repository; name one of <repo>/<id>, …`.

**The copy** is `--copy`, which must already hold a copy marker (`<dir> is not a Guildhall copy: it has no .guildhall-copy marker, and Clerk never writes one`), or the copy found from the working directory, refused with copy discovery's one line `no Guildhall copy found from <cwd>; pass --copy <dir> or cd into a copy`, which is also the answer under a copy's `state/`.
A copy with no store is refused as other reads refuse it.

**Read-only.** Clerk opens the store read-only and writes no receipt, takes no task lock, never fetches and never writes the copy marker.
The one file it may create is an `attempt.lock`, which live classification creates as `director status` does.
A writer holding the store's lock longer than `clerkLockWait` (the store wait other reads use) gives `the store is busy (the Director is writing); try again in a moment`, never an empty answer.

## Tests

`internal/cli/clerk_test.go` builds one marked copy through the store API with a fixed clock: a never-started task, a build running under this test as its live launcher with an answering endpoint and a fresh heartbeat, a pending approval with a plan summary, `wait: checks` with pull request #212, an `app` merge queue of three (front rebasing, a held entry with passed checks, an ordinary one) and a `lib` queue whose front waits on CI, a start-queue wait, a dependency wait, an unknown wait, a task bound to the repository's custom bug workflow, mixed priorities with ties, a title too long for the width, a task merged two days ago and one nine, and the same id in both repositories.
`TestClerkGolden` pins `guild`, `guild --all`, `decisions`, `queue`, `task` and `inspect` against `testdata/clerk-*.txt` and that no line is over 100 columns; `TestClerkWhereItStands` checks every phrase, the custom bug kind, the order, the cut title and the seven-day window; `TestClerkQueueDecisionAndEmpty` a front waiting on an open question, and every empty answer.
`TestClerkReadOnly` runs every subcommand and checks the store's receipts and every file of the copy but an `attempt.lock` are unchanged.
`TestClerkErrors` covers no copy, a start under `state/`, `--copy` of an unmarked directory (refused, with no marker written), an unknown id, an id in two repositories and `<repo>/<id>`, the usage errors, and a store a writer holds (the busy line, exit 1).
`TestClerkFormatting` pins spend, durations, cutting, wrapping and pull request numbers.
`internal/clerk`'s `TestFiles` and `TestCommandsAllowOnlyTheirSubcommand` pin the plugin's files and each command's one allowed subcommand; `internal/adapter/claude`'s `TestSessionFilesClerk` and `TestSessionCommand` the session files and both `--plugin-dir` flags, Lanterns first; `TestDirectorStartWritesClerk` that `director start` writes the plugin into the Claude workspace and a second start rewrites it.
These tests were written with the implementation, not red first.
