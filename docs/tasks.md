# Tasks

A task is one unit of work in one repository a Director owns.
The `guildhall task` commands create tasks in the Director's store and read them back.
Creating a task records it and binds the exact workflow and configuration it was accepted under (R1.62); it launches nothing, calls no model and starts no step.
For decisions and their answers, see the [decision commands](decisions-cli.md); `task start`, which advances a task, is in [running a task](running.md).

## Commands

Every command takes `--store <path>`, an existing store file made by [`guildhall store init`](store.md#guildhall-store-init), or a [copy](running.md#copies) with `--copy <dir>` or found from the current directory, whose `director.store` is the store and whose `config.toml` is `task new`'s `--config`.

- `guildhall task new --store <path> --config <director.toml> --repo <id> --title <text> [--detail <text> | --detail-file <file|->] [--kind feature|bug|hallmark] [--depends-on <repo>/<task>]... [--fixes <repo>/<task>] [--priority <level>] [--override <key>=<value>... --override-quote <text>] <task>` creates task `<task>` of the [kind](#task-kinds) given in repository `<id>` and prints the store's receipt as one JSON document; each `--override` is a [task override](#task-overrides), checked against the config before the store opens, so a refused one creates no task, and recorded after the task as `task override set` records it, quoting `--override-quote`; `task new --help` prints the usage with each kind's description, and exits 0.
  The title is a short name; the task's full statement, such as a bug's symptom and exact error, goes in its detail, given as text with `--detail` or read with `--detail-file` from a file, or from standard input for `-`, with the same rules as [`decide answer --answer-file`](decisions-cli.md) (a leading byte order mark is dropped, UTF-16 is refused and CRLF becomes LF).
  Both are optional, and giving both is a usage error.
  A title over 100 characters is refused with exit 1 before the configuration is read, naming `--detail` and `--detail-file`.
- `guildhall task priority --store <path> --repo <id> <task> <level>` sets an open task's [priority](#priority) and prints the receipt.
- `guildhall task hold-merge --store <path> --repo <id> <task>` [holds or unholds](#holding-a-merge) an open task's place in its repository's merge queue and prints the receipt.
- `guildhall task show --store <path> [--config <director.toml>] --repo <id> <task> --json` prints `{"task": ..., "attempts": [...], "documents": [...], "decisions": [...], "context": [...], "links": [...], "overrides": [...], "skipped_overrides": ..., "also_requires": ...}` for one task; `context` is its [task context](#task-context) entries, in number order, `links` its [task link](#task-links) records, adds and removes, in number order, each with `op`, `source` (`repository`, `task_id`, `name`, `path`, `sha256`), `text`, `author` and `added_at`, `overrides` its [task overrides](#task-overrides) in effect, the latest `set` record of each key, in key order, each with `number`, `key`, `value`, `text`, `author` and `added_at`, `skipped_overrides` those of them the config would not apply now, each with `key`, `value` and `reason`, judged only when show has a config, from `--config` or the copy's, and otherwise `null`, not judged (a config that does not load leaves it `null` with a warning on stderr), `also_requires` the [merge requirements](configuration.md#merge-requirements) its overrides add to the repository's, ones the repository has off, judged and `null` like `skipped_overrides`, and `design_rounds`, present once the task has had a [design round](#design-rounds), the rounds used.
- `guildhall task list --store <path> [--repo <id>] [--workflow <name>] [--open] --json` prints a JSON array of tasks, ordered by repository and then task id, for one repository or every repository in the store.
  `--workflow` keeps only the tasks bound to that workflow name, and `--open` only open tasks; before deleting `workflows/<name>.json` the Director checks that `task list --workflow <name> --open --json` prints `[]`, since an open task bound to it would strand, and nothing else refuses the deletion.
  A task in its repository's [merge queue](interpreter.md#the-merge-queue) also has a `merge_queue` object, the entry `director status` lists ([status](director.md#status)); it is absent for a task that is not queued.
- `guildhall task timeline --store <path> --repo <id> <task> --json` and `guildhall task files --store <path> --repo <id> <task> --json` are the [inspection reads](#inspection).
- `guildhall task finding rule --store <path> --repo <id> --outcome <document> --finding <id> --move <move> --reason <text> <task>` records a [Director ruling](#director-rulings) and prints its receipt.
- `guildhall task review --full --store <path> --repo <id> <task>` records a [full-review request](#full-review) and prints its receipt.
- `guildhall task cancel --store <path> --config <director.toml> --state <dir> --repo <id> --reason <text> <task>` [cancels](#task-cancel) an open task for good and prints one line per effect.
- `guildhall task reconcile --store <path> --config <director.toml> --state <dir> --repo <id> <task>` [records an open task done](#task-reconcile) whose pull request was merged outside Guildhall.
- `guildhall task grant create|revoke|list ...` records, revokes and lists [task grants](#task-grants); these take `--task <id>` rather than a positional task.
- `guildhall task context add --store <path> --repo <id> (--text <text> | --file <path>) <task>` adds an entry of [task context](#task-context) and prints its receipt; `guildhall task context list --store <path> --repo <id> <task> --json` prints the task's entries.
- `guildhall task link add|remove --store <path> --repo <id> --document <repo>/<task>/<name> (--text <text> | --file <path>) <task>` links another task's recorded document to a task, or unlinks it, as a [task link](#task-links) and prints its receipt; `add` also takes `--state <dir>`; `guildhall task link list --store <path> --repo <id> <task> --json` prints the task's link records.
- `guildhall task depends add|remove --store <path> --repo <id> --reason <text> <task> <repo>/<dependency>` adds a dependency to an open task, or removes one, after creation, as described in [changing dependencies](#changing-dependencies), and prints its receipt.
- `guildhall task override set --store <path> --config <director.toml> --repo <id> --key <key> --value <value> --text <text> <task>` gives a task its own value for a task-scoped setting as a [task override](#task-overrides) and prints its receipt; `unset` removes it and takes no `--config` or `--value`; `guildhall task override list --store <path> --repo <id> <task> --json` prints the task's override records.
- `guildhall task design --repo <id> --task <id> --brief <file|-> [--copy <dir> | --store <path> --config <file> --state <dir>]` adds a [design round](#design-rounds) to a feature task and advances it, printing where the task waits as [`task start`](running.md#task-start) does; it takes `--task <id>` rather than a positional task.

For every command but `list`, the positional task id may appear before or after flags.
`show`, `list`, `timeline`, `files` and `context list` open the store read-only and change nothing.
`--json` is required on all five, so a later human-readable format is not a breaking change.

Repository and task ids are lowercase letters, digits and hyphens, starting with a letter.
A title is non-blank UTF-8 of at most 100 characters (runes); a detail, when given, is UTF-8 of at most 64 KiB.
`task show` and `task list` print a task's detail as `detail`, absent when it has none.

Exit codes are those of the messages commands: 0 on success, 1 when the configuration, the workflow or the store refuses or fails, and 2 on a usage error.
Errors are written to standard error and nothing is written to standard output.

## Task kinds

A task is created as a **task kind**, which chooses its workflow; it is not a step kind, which is a built-in step type in a workflow.
`--kind` chooses it; with no `--kind`, a task with `--fixes` is a `bug` and any other a `feature`.
`--kind feature --fixes <repo>/<task>` is a feature that still records the task it fixes.
An unknown `--kind` is a usage error.

| Task kind | Workflow | What it is for |
|---|---|---|
| `feature` | The repository's `workflow` setting, `default` when it names none | New or changed behaviour: grill, research, plan, the Guildmaster's acceptance, build, review, publish, validate and deliver. |
| `bug` | The repository's [`[repository.kinds]`](configuration.md#repositories) `bug` workflow, the shipped `bug` when it names none | A fix for a defect in behaviour that should already work, with no Guildmaster stage: the default's steps without grill and the Guildmaster's acceptance, so research, plan, build, review, publish, validate and deliver, merging under the repository's merge requirements. Research reproduces the defect from the task title and detail and finds its cause, or fails with what it found; plan proceeds on recommended answers, or fails with a question that needs the Guildmaster; build writes a regression test that fails before the fix and passes after it, and review checks that test. |
| `hallmark` | The repository's `[repository.kinds]` `hallmark` workflow, the shipped `hallmark` when it names none | A report-only review of the whole repository, launched when a [hallmark is due](director.md#maintenance): one read-only `hallmark` stage, under the `researcher` profile with the `guildhall-hallmark` skill, is given the repository's previous hallmark report as `previous` and writes a ranked report of PR-sized follow-ups. Intake fails the stage if it leaves a tracked change or moves HEAD, and the task finishes as done with no pull request ([decision](decisions/report-only-workflows.md)). |

The descriptions are in the binary, `task new --help`, not in the workflows, so rewording them changes no digest.
Every Artisan prompt shows the task's title and detail, quoted as text after its header; a bug task has no grill, so its title and its detail, with the symptom and the exact error, are the only statement of the defect its research is given.
A failed research or plan attempt of a bug task carries its findings or its question in its summary, which the Director reads with `task show` and brings to the Guildmaster before any `step retry`.
The task records no kind: its bound workflow's name says what it was created as.
A repository with no `kinds` table, and a `task new` with neither `--kind` nor `--fixes`, bind exactly the workflow they did before kinds.

## The bound workflow

`task new` loads the configuration with the [configuration loader](configuration.md), including a Steward-owned `managed.toml` beside it, and finds the repository there.
The task's kind names the effective workflow: the repository's `workflow` setting, `default` when it names none, for a feature, and its `kinds` table's `bug` or `hallmark`, the kind's own name when it names none, for a bug or a hallmark.
A name resolves to:

- the definition of that name shipped with this Guildhall release, for `default`, `bug` and `hallmark`.
  Any `workflows/default.json`, `workflows/bug.json` or `workflows/hallmark.json` beside the configuration is ignored.
- the file `workflows/<name>.json` in the configuration file's directory for any other name, and the file's `name` must equal it.

The definition is parsed with `workflow.Parse`, the repository's `workflow_overrides` are applied with `workflow.ApplyOverrides`, and the result is checked with `workflow.Validate` (R1.59); no role profile is written into it.
The effective workflow is then checked with `config.CheckArtisanAdapters`: every named profile must be declared in the loaded Director configuration or [shipped](configuration.md#profiles), and every Artisan step must resolve a profile.
A research, plan, build or Artisan review step with no `profile` runs under its [role profile](configuration.md#profiles), `researcher`, `planner`, `builder` or `reviewer`, the copy's own or the shipped one, resolved at use and outside the digest.
An Artisan step whose type has no role, such as `refute` or `document-review`, and that names no profile is refused with the workflow and step named and a remedy to set a profile on that step; no default adapter, real or fake, supplies a missing profile.
The shipped `default` and `bug` workflows name no profiles, so any copy runs them with no custom workflow and no `[profile.*]` section, binding the shipped revision byte for byte, which needs no approval.
For deterministic tests or demos, name a profile whose `adapter` is `fake`, or declare the role profiles with it; other actors need no Artisan profile.
A missing or unreadable file, a parse error, a refused override, a validation problem, an undeclared profile or an Artisan step that resolves no profile refuses the task with the problems named, and the store is not opened, so nothing is written.

The effective workflow must then be [authorized](configuration.md#enforcement-at-task-creation): shipped with the release, approved by the Guildmaster for its exact digest, or a change from a shipped or approved definition that meets the review floor and weakens nothing.
An unauthorized workflow refuses the task with its digest and what it weakens named, and writes nothing.
The task binds `workflow.Revision` of the overridden definition, its name and digest, so two repositories with different overrides of one workflow bind different digests.
It also binds the configuration digest: `sha256:` and the lowercase hex sha256 of the loaded configuration's canonical JSON encoding.
The digest covers the local and managed settings together and not the files' formatting; it is computed by this release, so a release that adds configuration fields may compute a different digest for the same files.
Later edits to either file, or to the workflow file, apply to new tasks only.
Every later advance resolves the task's workflow by its bound name, not the repository's current `workflow` setting, so changing that setting leaves open tasks on the workflow they were created with ([running](running.md#what-an-advance-runs-over)).
Editing the bound workflow's file, the repository's `workflow_overrides` or the profiles its steps were defaulted from changes the digest, and the task's next advance is refused naming its bound workflow; keep a workflow's file until every task bound to it has closed.
A task bound to `default` or `bug` under an earlier release keeps resolving after an upgrade changes the shipped bytes: the bound name also resolves to the earlier releases' shipped revisions of it, with the repository's current overrides, though none of them is admitted for a new task ([decision](decisions/retired-shipped-revisions.md)).

## Dependencies

Each `--depends-on <repo>/<task>` names a task of this Director, in any repository, that must already exist in the store.
An unknown dependency refuses the task and writes nothing; a task cannot depend on itself.
The dependency order is preserved as given.
The task record keeps this creation list unchanged.
The Director can add or remove dependencies afterwards with [`task depends`](#changing-dependencies); each change is its own record.
A task's **effective dependencies** are its creation list with those records applied in number order.
The gate, `--ignore-deps`, `task cancel`'s message to dependents, `prune`, the clerk and `task show` all read the effective dependencies.

A dependent task waits for its dependencies: its grill runs at once, but research, or whatever its first step not run by the Director is, does not start until every dependency is `done`, that is, merged.
That is the [dependency gate](interpreter.md#the-dependency-gate): while it holds the task, advancing it records nothing and prints `{"wait":"dependencies","step":"<step>"}`, and the Director gets one `dependency-held` message for each change in the dependencies' coarse state (in progress, waiting for the Guildmaster or the Director, cancelled), never one per advance.
The session supervisor's [dependency watcher](director.md#dependencies) advances the task once its dependencies merge, with no model turn, and research then runs on a branch fast-forwarded to the main they merged into; a branch that already has commits is not rebased then, only later by the [merge queue](interpreter.md#the-merge-queue) at the front, or at validate when its pull request conflicts ([tiered rebase](interpreter.md#tiered-rebase)).
While a dependency waits in the merge queue, its state in `dependency-held` reads `queued for merge`, or `merge held` after [`task hold-merge`](#holding-a-merge).
A task already past research, with an attempt of a step not run by the Director, is never held, so a loop, retry or send-back back to research runs while a dependency is still open.

The Director can start a held task anyway with [`task start --ignore-deps --reason <text>`](running.md#task-start), the **dependency override**.
It is recorded as its own record, never by changing the task (R1.62), lasts for the rest of the task's life, starts the task from the current main and shows with its reason in `task timeline`.
A cancelled dependency never merges, so a task that depends on one starts only with the override, or once [`task depends remove`](#changing-dependencies) drops the cancelled dependency; [`task cancel`](#task-cancel) tells the Director about each open dependent with a `dependency-cancelled` message.

### Changing dependencies

```
guildhall task depends add    --store <path> [--config <director.toml>] --repo <id> --reason <text> <task> <repo>/<dependency>
guildhall task depends remove --store <path> [--config <director.toml>] --repo <id> --reason <text> <task> <repo>/<dependency>
```

`--copy <dir>`, or the copy found from the current directory, stands in for `--store` and `--config`.

`add` makes `<repo>/<dependency>` one of the task's effective dependencies.
`remove` drops one, including a dependency the task was created with.
The main use is a replaced task.
When a task is cancelled and a replacement is created, each dependent runs `remove` for the old task and `add` for the new one, so it follows the replacement instead of being cancelled and recreated.

- The dependency is `<repository>/<task>`, as `--depends-on` takes it, of any repository of the copy.
- The reason, `--reason`, must be non-blank UTF-8 of at most 64 KiB.
- Each change records the task's next dependency record, numbered from 1 per task, adds and removes alike.
  The record holds `op`, `dependency`, `text` (the reason), author `director` and `added_at`.
- Each change prints its receipt: record `task-dependency/<n>`, mutation `task-dependency:<repo>:<task>:<op>:<dep repo>:<dep task>:<prior>`.
  The mutation is hashed as for `task new` when longer than 128 bytes.
  `prior` is how many records the task already has for that dependency.
- Running the same command again, while its record is still that dependency's latest, prints the original receipt and records nothing.
- These are refused with exit 1, and nothing is written:
  - a task that is unknown or not open;
  - a dependency that is unknown, or is the task itself;
  - an `add` of a cancelled dependency, or of one already effective;
  - an `add` that would make a cycle; the error names the path, such as `app/t-1 -> app/a -> lib/b -> app/t-1`;
  - a `remove` of a dependency that is not effective;
  - a record made meanwhile for the same dependency.
- A `done` dependency may be added; it is satisfied at once.
- A usage error exits 2.

A change takes effect at the task's next advance through the gate; it never stops or restarts running work.
A held task whose last unmet dependency is removed is released on the [dependency watcher's](director.md#dependencies) next tick.
A task that gains an unmet dependency before the gate is held when it reaches it.
A changed set of unmet dependencies sends a new `dependency-held` message.

An `add` that holds nothing is still recorded, and its receipt carries a `note`:

- `past the dependency gate; this change holds nothing until the task is recreated`: the task already has an attempt of a step not run by the Director, so the gate never holds it again.
  The task's bound workflow is resolved with `--config`, or the copy's, to tell.
- `dependency override in force; this task is not held`: the [dependency override](#dependencies) lasts for the task's life.

`task show` and `task list` print the effective dependencies as `depends_on`.
`task timeline` lists every record under `dependencies` and keeps the creation list in `task`.

`--fixes <repo>/<task>` names the earlier task whose defect this one fixes, which must already exist; publish then adds "Fixes a defect introduced by task `<repo>/<task>`." to the pull request body ([quality rubric](quality.md#running-it)), whatever the task's kind; without `--kind` it also makes the task a [bug](#task-kinds).

## Priority

Every task has a priority, one of six levels:

| Level | Word |
| --- | --- |
| `p0` | `urgent` |
| `p1` | `highest` |
| `p2` | `high` |
| `p3` | `normal`, the default |
| `p4` | `low` |
| `p5` | `someday` |

A level may be given as `p1`, `1` or `highest`, in any letter case; anything else is a usage error.
`task new --priority <level>` sets it when the task is created, and `task priority <task> <level>` changes it on an open task at any time.
The priority is its own record, `task-priority`, never a change to the task (R1.62); it holds the latest level and every level set, and `task timeline` shows the levels with when each was set.
`task list` and `task show` print `priority` on each task, and setting the level already in force prints the receipt that set it and records nothing.

Priority orders the repository's [merge queue](interpreter.md#the-merge-queue): a lower number comes first, and tasks of the same priority keep the order in which they first became ready to merge.
A change reorders the tasks waiting behind the front at once; it never takes the front from the task holding it.

## Holding a merge

`task hold-merge <task>` toggles a recorded hold on the task's merge, receipt record `merge-hold`: run once it holds, run again it unholds.
A held task stays authorized and keeps its ready time, but the [merge queue](interpreter.md#the-merge-queue) skips it; a held front gives up the front at its next advance, and the next task in order may claim it.
While a Director session runs, its [merge-queue watcher](director.md#merge-queue) runs that advance within one `checks_refresh`, whatever the task last waited on, so a front stuck on a failing push or an unanswered decision still yields the queue.
Unholding lets the task claim the front again when it is first, in a new stay with three more rebase rounds.
Every change shows in `task timeline`, and `director status` and `task list --json` show `held`.

This is not the `hold` answer on a merge decision.
That answer refuses the merge it was asked for, and the task waits for the Director; `hold-merge` acts on a merge already authorized, and the task waits in the queue.
A held task also keeps its dependents waiting, whose `dependency-held` message reads `merge held`.

## Task cancel

`task cancel --reason <text> <task>` ends an open task for good without delivering it ([decision](decisions/task-cancel.md)).
The Director first asks the Guildmaster, naming each task, and runs it only after their yes, one command per task; it cannot be undone, and work that is wanted again is a new task.
`--reason` is required, non-blank UTF-8 of at most 64 KiB, and the repository must be listed in the config, as for `task start`.
It takes the `--store`, `--config` and `--state` of `task start`, or a copy, and holds the task's lock for its whole run, so it never interleaves with an advance or a merge.

In order, it:

1. refuses a `done` task, and prints that a `cancelled` task is already cancelled, with its reason, and exits 0;
2. refuses a task whose deliver recorded its merge, a `<decision>-merge` document: its work merged or is merging; a [multi-PR task](#multi-pr-tasks) is refused only while its current slice is merging, that is, the current slice has a merge record or the slice merged last has not finished closing, since the next one has recorded nothing yet, and advancing the task finishes it;
3. stops each running attempt: an Artisan attempt through its owner, as [`artisan stop`](artisan.md#commands) does, and a Director stage's attempt, which has no owner, by recording it `stopped`; when an Artisan's stop cannot be proven the cancel is refused and the task stays open, so a cancelled task never has a live Artisan;
4. closes each pull request the task's `-pr` documents record with the comment `Cancelled by Guildhall: <reason>`, keeping its branch; a closed one is left as it is, and one merged on the forge refuses the cancel, after only the stops above: its work landed, so the [outside-merge rule](#merged-outside-guildhall) records the task `done`, and the cancel exits 1 with `pull request <url> of task <repo>/<task> is merged; its work landed, so the task is recorded done, not cancelled`;
5. voids each open decision with the reason `Task cancelled: <reason>`: a void decision leaves `decide list`, `director status` and the dependency gate's states, `decide show` prints it with `"void":true`, and it can no longer be answered;
6. takes the task out of the [start queue](interpreter.md#the-start-queue) and the [merge queue](interpreter.md#the-merge-queue), releasing the front if it holds it, and clears its [merge hold](#holding-a-merge);
7. sends the Director one `dependency-cancelled` message for each open task, in any listed repository, that [depends](#dependencies) on it, by its effective dependencies; the dependents are not cancelled, and one already held by the dependency gate gets its `dependency-held` message too at its next advance;
8. records the task `cancelled`, with its `cancel_reason` and `cancelled_at`.

A multi-PR task with slices merged is cancelled too: step 4 closes only the unmerged slices' pull requests, a merged slice's stays merged and stands, and the cancel prints a line naming each merged slice and its pull request after recording the task `cancelled`, never `done`.
The reason still comes from the Guildmaster's own words, as for every cancel.

Each step's write has a mutation id derived from the task, and the state change comes last, so a cancel that fails part-way leaves the task open and running the same command again finishes it: nothing is stopped, closed, voided or sent twice.
The task's branch and working directory are kept, so its work can be recovered.

A cancelled task leaves `task list --open`, and `task list` and `task show` print its state, reason and time.
It contributes nothing to `director status` but its unread messages, until they are read.
A cancel counts toward `handoff_after_tasks` as a merge does.
Stopping an attempt makes its launcher advance the task; `task start` and `step done` of a cancelled task print `task <repo>/<task> is cancelled; nothing to advance` and succeed, so no `advance-failed` message follows; of a done task they still fail.
`decide answer` refuses a cancelled task's decisions; a done task's, such as an audit an earlier release opened, are answered as before.
Cancel never records `cancelled` for a task whose work landed.

## Merged outside Guildhall

A task's pull request may be merged on the forge by hand, outside Guildhall.
The task has then **landed outside Guildhall** when it has no `-merge` document, so deliver has not merged it, and the forge now reports one of the pull requests its `-pr` documents record as merged, at whatever head; every recorded pull request is read, not only the latest, so an earlier one merged by hand counts though a later one was closed.
A pull request only closed never counts.
A merge of deliver's own whose record a crash lost, the pull request merged at the head of deliver's latest merge, not rebased and not held, is left to deliver, which records it as before.

When a task has landed outside Guildhall, Guildhall, in order:

1. records the forge's merged pull request, with its head and `merge_commit`, as `outside-<number>-merge`, so `task cancel` sees the task as merged;
2. voids each open decision with the reason `Task done: pull request <url> merged outside Guildhall`, except an `audit` an earlier release opened, which stays answerable;
3. sends the Director a `merged` message, id `<repo>.<task>.merged.outside.<number>`, naming the pull request, the forge's head, its merge commit and the head Guildhall last recorded for it, so `task timeline` and `director status` show the merge;
4. retires the working directory at the forge's head; a head Guildhall never fetched keeps it, with a `workspace-kept` message, as for any merge;
5. takes the task out of the [merge queue](interpreter.md#the-merge-queue), releasing the front if it holds it, and clears its [merge hold](#holding-a-merge);
6. records the task `done`.

Like a merge by deliver, it opens no [audit](quality.md#honesty-and-evidence-g): the Guildmaster's audit is retired ([decision](decisions/retire-quality-audit.md)).

Every write's mutation id is keyed on the pull request's remote and number, so a run that fails part-way is finished by the next from the recorded `outside-<number>-merge`, sending nothing twice.
The forge must read pull requests (GitHub does); a forge that cannot never applies the rule.

A [multi-PR task](#multi-pr-tasks) applies the rule to its current slice alone: the rule reads only that slice's recorded pull requests, while the slice has no merge record.
A merge of a slice before the last is recorded as that slice's merge, `outside-<number>-merge` for slice 1 and `outside--s<k>-<number>-merge` after, voids the open decisions, sends a `merged` message naming the slice, retires the slice's working directory and takes the task out of the merge queue, but leaves the task open: the advance goes on with the next slice.
Only the last slice's merge records the task `done`.

Every advance applies the rule first, after taking its event's result and before any step runs, to a task with `-pr` documents and no `-merge` document, at the cost of one `gh pr view` per recorded pull request; that advance prints `{"wait":"done",...}`.
So publish never opens a new pull request for a branch whose pull request merged, and validate and the merge-queue front never read a pull request that moved when it was merged.
While one of the task's attempts runs the rule waits, and applies at the advance its result brings.
A pull request read that fails fails the advance.

### Task reconcile

`task reconcile <task>` applies the rule now, for drift the Director finds: it takes the flags of `task cancel` but `--reason`, the repository must be listed in the config, and it holds the task's lock.
It stops the task's running attempts as `task cancel` does, then reads the forge and prints `done: pull request <url> merged outside Guildhall` or `no change: no recorded pull request is merged`, exiting 0 either way.
For a multi-PR task whose slice before the last merged it prints `slice <k> merged: pull request <url> merged outside Guildhall; the task stays open for its next slice`, which the task's next advance starts; it never closes a multi-PR task before its last slice.
A `done` or `cancelled` task prints its state and changes nothing.

## Multi-PR tasks

Work larger than one pull request is one task whose accepted spec lists its slices under `## Slices`: a numbered list, each entry a bold title, what that pull request delivers and its expected size ([decision](decisions/multi-pr-tasks.md)).
The plan writes the list into the spec, Guildhall parses it strictly when it records the plan's result, and the Guildmaster accepts the spec, slices included, once.
A malformed list, more than one slice under a workflow revision that declares no slice range, such as a retired default or the bug workflow, and a revised spec that changes or drops a merged slice's entry fail the plan attempt with the reason, and no spec is recorded.
A spec with no `## Slices`, or with one entry, is a one-slice task, and runs exactly as before.

The shipped default workflow declares `"slices": {"from": "build", "to": "deliver"}`: build, review, publish, validate and deliver run once per slice, in order.
The current slice is the slices with a merge record plus one, worked out from the records at every advance, so nothing stores a cursor.
Slice 1 records everything under today's names and publishes `guildhall/<task>`; slice k from 2 on records its steps as `<step>--s<k>`, such as `build--s2-1` and `deliver--s2-1-merge`, and publishes `guildhall/<task>.s<k>` from a fresh worktree, `<state>/<repo>/<task>/work.s<k>`, cut from main after the slice before it merged.
`step retry`, `step send-back` and `step done` name such a step by that id.
Every slice reads the same `accept.spec`, so no slice asks for a new acceptance, and every slice pull request meets the repository's merge requirements and goes through the merge queue as any pull request does.
Its title is `<slice title> (slice k of n)`, and its body quotes the spec's goal, the slice's entry and every slice with the pull request of each merged one.

A slice's merge before the last records the merge, sends a `merged` message naming `slice k of n`, retires that slice's branch and working directory and takes the task out of the merge queue, so the next slice joins it afresh, with its stays and hold reset; then the same advance starts the next slice's build.
The task stays `open`, and only the last slice's merge records it `done`.
A crash between a slice's merge record and the next slice's first attempt resumes at the next slice, finishing the merged slice's close first.
Each slice merge sends a `merged` message, so it counts toward `hallmark_after_merges`; `handoff_after_tasks`, `--depends-on` and `--until-merged` count or release only when the task closes.

A spec that turns out wrong part-way goes back to plan with `step send-back`; the revised spec keeps every merged slice's entry byte for byte and may rewrite, add or drop the slices still to come, the Guildmaster accepts it, and the current slice's steps, bound to the old spec, run again under the new one.
Merged slices' records are never read again.
`director status` gives such a task a `slices` object, `{current, total, items: [{index, title, state, pull_request}]}`; Clerk's `/guild` shows `slice k of n` beside where it stands, `/task` and `/inspect` show the current slice and list every slice with its title, state and pull request, and the Artisan's Herdr workspace is labelled `└ <task> · slice k/n · <step> #<n>`.
GitHub stacked pull requests are not used: each slice starts after the one before merged ([why, and when stacking may be reconsidered](decisions/stacked-prs-not-yet.md)).

## Retries

`task new` records the mutation `task:<repo>:<task>` when that string is at most 128 bytes.
For longer strings it records `task:sha256:<digest>`, using the lowercase hex SHA-256 of the full original string.
Repository and task ids cannot contain `:`, so the input is unambiguous; short ids remain unchanged and every valid pair has a bounded, deterministic mutation id.
Running the same `task new` again, for example after losing its output, prints the original receipt and writes nothing, even after a restart.
Reusing a task id with a different title, dependencies, workflow revision, such as another `--kind`'s, or configuration digest is refused with the store's mutation conflict and changes nothing.

The store has one writer at a time; each command holds it only for its own operation (see [the store](store.md#one-writer)).

## Inspection

The Director inspects a task through read-only commands rather than by opening worker files ([inspection reads](decisions/inspection-reads-from-store-receipts.md)).
`timeline` and `files` read only the store: one read transaction, with the store opened read-only, so they never write, never create a store, and wait for the writer's lock like any other read.
Each prints one JSON document whose `as_of` label says exactly what it describes:

- `partial`: always `true`; `excluded` lists live worker state, worker files and transcript, none of which these store-only reads inspect.
- `sequence` and `committed_at`: the newest mutation committed anywhere in the store when the read began.
  Two reads of the same store with the same sequence saw the same records.
- `workflow`: the workflow revision (`name`, `digest`) the task is bound to.

Times are commit times of the store's [receipts](store.md#mutations-and-receipts), in UTC; they record when Guildhall wrote a record, not when a worker did the work.
An unknown repository or task fails with exit 1 and the store's not-found error.

`task timeline` prints:

- `omitted_kinds`: the record kinds this version leaves out, each with why; empty now that findings and rulings are recorded.
- `task`: the task record, with `depends_on` the list it was created with.
- `attempts`: every stage attempt with its outcome (absent while running), `started_at`, `finished_at` and `duration_seconds` between them, ordered by when they started.
- `decisions`: every decision with its kind, subject, options, question and the Guildmaster's exact words (`answer`, `choice`, `note`), plus `opened_at`, `answered_at` and `wait_seconds` between them, ordered by when they opened.
- `findings`: every review outcome document with its `summary` (absent for a /1 outcome), its findings (`id`, `rule`, `file`, `line`, `summary`), its `suggestions` (`id`, `file`, `line`, `summary`) and its `dispositions` of the previous round's findings (`finding`, `state`, `reason`), read from the store's projection, with `recorded_at`, ordered by when they were recorded.
  A projection recorded from a /1 outcome before /2 keeps its rule-less findings in `findings` and the /1 state names `still-open` and `withdrawn`.
- `rulings`: every [Director ruling](#director-rulings) with its document, finding, `move` and `reason`, `contested` and `contested_by`, and `ruled_at` and `contested_at`, ordered by when they were made.
- `full_reviews`: every [full-review request](#full-review) with its `number`, `consumed_by` (absent while pending), `requested_at` and `consumed_at`, in number order.
- `context`: every entry of [task context](#task-context) with its `number`, `text`, `author` and `added_at`, ordered by when it was added.
- `links`: every [task link](#task-links) record with its `number`, `op`, `source`, `text`, `author` and `added_at`, ordered by when it was recorded; a removed link stays listed beside its removal.
- `overrides`: every [task override](#task-overrides) record with its `number`, `op`, `key`, `value` (on a `set`), `text`, `author` and `added_at`, ordered by when it was recorded; an unset override stays listed beside its unset.
- `dependencies`: every [dependency record](#changing-dependencies) with its `number`, `op`, `dependency`, `text` (the reason), `author` and `added_at`, ordered by when it was recorded.
- `design_rounds`: the [design rounds](#design-rounds) used, counted from their briefs; absent when the task has had none. Each round's designer attempts and its decision are among `attempts` and `decisions`.
- `dependency_override`: the Director's [dependency override](#dependencies), with its `reason` and `overridden_at`; absent when the task has none.
- `priority` and `priority_changes`: the task's [priority](#priority) now and every level set, each with `set_at`.
- `merge_holds`: every [hold-merge](#holding-a-merge) value set, `held` true or false, each with `set_at`.
- `merged`: once the task merged, the `merged` message the engine sent, its `message` id, `text` and `merged_at`; the text says what allowed the merge, for example "merged by carried-over approval of deliver-1 after no_conflicts rebase, deliver-2-authorized".
- `events`: every receipt of the task in commit order, each naming its operation, mutation id, sequence, commit time and the record it wrote (`record`, for example `attempt/build/2`, `decision/accept-plan` or `message/q-1`).
  These are the moves made on the task, including messages sent and acknowledged for it.

`task files` prints `documents`: every document reference of the task with its relative path, its recorded `sha256` and `recorded_at`, in name order.
The digest is the one recorded when the reference was written; `files` does not open or re-hash the document.
Once [`guildhall prune`](running.md#pruning) has reclaimed the task's files, it also prints `pruned`: the prune record's `pruned_at`, `bytes_freed`, `worktree`, `kept`, `complete` and `runs`, with a `summary` such as `pruned on 2026-10-16`, followed by `; worktree kept: <reason>` (or `; not a worktree: <reason>`) when the prune left the worktree; it is absent for a task never pruned.
Documents are never pruned, so their references stay valid.

Receipts committed before receipts named their record have no `record`; they still appear in `events`, but cannot supply record timestamps.
Unknown timestamps are omitted from the JSON.
Timestamps are matched by operation: an attempt finished or a decision answered after an upgrade can have a known completion time with an unknown start.
Durations require both endpoints; attempts and decisions without a start time follow the timed ones in store order.
The timeline includes every recorded event kind in commit order.
Findings come from the projection the engine records with each review outcome document, so the timeline never opens the outcome file.
Reading a task's diff, process status or transcript needs its worktree and adapter, and is separate work.

## Director rulings

The Director may rule on one finding, or one suggestion, of a review outcome document instead of waiting for the reviewer (report §5.4).
`task finding rule` records the ruling with the mutation `ruling:<repo>:<task>:<document>:<finding>`, hashed as for `task new` when longer than 128 bytes, so a retry prints the original receipt.
`--outcome` names the outcome document as the timeline's `findings` show it, such as `review-2-outcome`, and `--finding` one of its finding or suggestion ids.
`--move` is one of:

- `out-of-scope`: the finding is outside the accepted spec; the reason cites the spec section;
- `duplicate`: the finding repeats another one;
- `defer`: the item moves to a follow-up task; only a suggestion, or a /1 finding that cites no rule, may be deferred.

A finding is ruled on once per document; a conflicting retry, an unknown document or finding, a whitespace-only reason, an unknown move, deferring a rule-cited finding or a task that is not open is refused with exit 1 and writes nothing.
An omitted or empty required flag, including `--reason`, is a usage error (exit 2).
A ruling never resolves a finding and never changes a verdict.
The one exception is the `nothing_open` merge requirement: a `disputed` finding with a standing ruling, of any move, no longer counts for it ([decision](decisions/review-outcome-2.md)).
The [interpreter's Director-ruling contract](interpreter.md#director-rulings) defines when rulings enter an attempt's immutable inputs, how they are contested, and when disputes reach the Guildmaster.

## Full review

The Director may force the task's next code review round to review the whole head, never only a rebase's resolution ([rebase-aware review](interpreter.md#rebase-aware-review)).
`task review --full` records a full-review request, receipt record `full-review/<n>`, with the mutation `full-review:<repo>:<task>:<n>`, hashed as for `task new` when longer than 128 bytes, where `n` numbers the task's requests from 1.
At most one request is pending: a retry while it is pending prints the original receipt, and once a round consumed it the next command records request `n+1`.
`--full` is required, so a later form of `task review` is not a breaking change; omitting it, `--repo` or the task, or passing `--json`, is a usage error (exit 2).
A new request for an unknown task or one that is not open is refused with exit 1 and writes nothing; replaying a pending request still returns its original receipt.
The command does not launch a review, invalidate a completed review or change an already recorded attempt binding.
The request applies to the next Artisan code review attempt whose binding has not yet been recorded, not to a human review decision or a document review.
See the [interpreter's rebase-aware review contract](interpreter.md#rebase-aware-review) for consumption, replay and scope recording.

## Task context

Task context is what the Guildmaster wants a task's Artisans to know that its title does not say: requirements, constraints or clarifications about that one task.
The Director records it on the task, quoting the Guildmaster where the words are theirs, rather than in a `guildmaster` [Director note](director.md#notes), which no Artisan reads.

```
guildhall task context add  --store <path> --repo <id> (--text <text> | --file <path>) <task>
guildhall task context list --store <path> --repo <id> <task> --json
```

`--copy <dir>`, or the copy found from the current directory, stands in for `--store`.

- `add` records the task's next entry, numbered from 1, with author `director` and the time it was added, and prints its receipt, record `task-context/<n>`, mutation `task-context:<repo>:<task>:<sha256 of the text>`, hashed as for `task new` when longer than 128 bytes; running the same command again prints the original receipt and records nothing. The text comes from exactly one of `--text` and `--file`, which names a file (`--file -` is not supported); it must be non-blank UTF-8 of at most 64 KiB. A task that is unknown or not open is refused with exit 1 and nothing is written.
- `list` prints the task's entries as a JSON array in number order; it opens the store read-only.

Entries are append-only: none is edited or removed, and a later entry supersedes an earlier one only by saying so in its text.
Adding context launches, re-runs and invalidates nothing.
Every Artisan attempt of the task bound after an entry is added is given every entry recorded by then as its `context` input, one Markdown document `context-<k>` (`tasks/<repo>/<task>/context-<k>.md`) for a task with `k` entries, each entry under `## Context <n>` with its time and author and its text fenced, so it is read as data ([engine-added inputs](interpreter.md#rounds-and-loops)).
An attempt already bound, running or recorded but not yet launched, keeps the inputs it was given, and a finished stage is not stale because of new context.
To get context into a finished stage, send it back with [`step send-back`](step-send-back.md) or retry it with `step retry`; either way its next attempt is bound with the context.
A task with no entries is given no `context` document.

## Task links

A task link gives a task's Artisans another task's recorded document, such as the spec a task's title names, which their sandbox cannot otherwise read.
The Director records it when the Guildmaster asks, quoting their words, instead of pasting the document into task context.

```
guildhall task link add    --store <path> --state <dir> --repo <id> --document <repo>/<task>/<name> (--text <text> | --file <path>) <task>
guildhall task link remove --store <path> --repo <id> --document <repo>/<task>/<name> (--text <text> | --file <path>) <task>
guildhall task link list   --store <path> --repo <id> <task> --json
```

`--copy <dir>`, or the copy found from the current directory, stands in for `--store` and, for `add`, `--state`.

- `--document` names one recorded document exactly: its repository, its task and its record name, as `task show` lists them under `documents`. An Artisan output is named `<step>-<n>-<port>`, so "d14's plan-5 spec" is `guildhall/d14/plan-5-spec`. Moving references such as "latest spec" are not accepted. Any recorded document of any task in any repository of the copy may be linked, whatever the task's state, done, cancelled and pruned included, since [prune](store.md#pruned-tasks) keeps every recorded document; a task's own documents cannot be linked, since its attempts reach them already. Code heads and branches cannot be linked; `--depends-on` orders tasks on code.
- `add` checks the document's file under `<state>/documents` against its recorded digest first, refusing a missing or changed file with an error naming the document; then it records the task's next link record, `op` `add`, with the source's `path` and `sha256` pinned. `remove` records `op` `remove`. Records are numbered from 1 per task, adds and removes alike, with author `director` and the time recorded, and each prints its receipt, record `task-link/<n>`, mutation `task-link:<repo>:<task>:<op>:<src repo>:<src task>:<name>:<prior>`, hashed as for `task new` when longer than 128 bytes, `prior` being how many records the task already has for that document. Running the same command again, while its record is still that document's latest, prints the original receipt and records nothing; removing and adding again records new records.
- The text, the Guildmaster's words, comes from exactly one of `--text` and `--file`, which names a file (`--file -` is not supported); it must be non-blank UTF-8 of at most 64 KiB. A task that is unknown or not open, a document that is not recorded, an `add` of a document already linked, a `remove` of one not linked and a record made meanwhile for the same document are refused with exit 1 and nothing is written.
- `list` prints the task's link records as a JSON array in number order; it opens the store read-only.

For each document the latest record wins: the task's linked documents are those whose latest record is an `add`, in the order of those adds.
Linking or unlinking launches, re-runs and invalidates nothing; run `task link add` straight after `task new`, before `task start`, so the first attempt gets the link.
Every Artisan attempt of the task bound afterwards, Director stages and design-round designers included, is given on port `linked` first an index document `links-<m>` (`tasks/<repo>/<task>/links-<m>.md`, for a task with `m` link records) listing each linked document's link number, source, the path it is found at among the attempt's inputs, its digest and the Guildmaster's words fenced, then each linked document as recorded, under its own path, such as `inputs/tasks/guildhall/d14/plan-5/spec.md` ([engine-added inputs](interpreter.md#rounds-and-loops)).
The attempt's binding, `<step>-<n>-inputs`, records exactly which linked documents it was given and their digests.
Before an attempt is bound, every linked file is checked against the digest its link pinned; a missing or changed file refuses the binding, and the advance fails with an error naming the task, the link and its source, such as `task guildhall/d70: link 2 guildhall/d14/plan-5-spec: file missing`, so no attempt runs without a document its task links.
An attempt already bound keeps the inputs it was given, and a finished stage is not stale because of a new or removed link; to reach it, `step send-back` or `step retry` it.
A task with no linked documents is given no `linked` input.
Rebaser and conflict-resolution attempts and Guildmaster questions are given neither linked documents nor task context.

## Task overrides

A task override gives one task its own value for a [task-scoped setting](configuration.md#setting-scopes), such as `default_effort`, recorded by the Director with the Guildmaster's words ([decision](decisions/task-overrides.md)).
The Director records one only when the Guildmaster asks; an Artisan cannot, and no Director sets one on its own.

```
guildhall task override set   --store <path> --config <director.toml> --repo <id> --key <key> --value <value> --text <text> <task>
guildhall task override unset --store <path> --repo <id> --key <key> --text <text> <task>
guildhall task override list  --store <path> --repo <id> <task> --json
```

`--copy <dir>`, or the copy found from the current directory, stands in for `--store` and, for `set`, `--config`.

- `set` first loads the copy's config and asks it whether `<key>` = `<value>` may override a setting for one task (`CheckTaskOverride`). An unknown key, a step key such as `step.build.effort`, a machine-wide setting, an excluded one (`[repository.merge]`, `[director]` and repository identity), a key a managed setting pins without `overridable = true`, and a value the setting's check refuses are each refused with exit 1, printing the registry's reason unchanged, such as ``guildhall: task override set: `lock_wait` is machine-wide; it cannot be overridden per task: not overridable per task``, and nothing is written.
- `unset` reads no config, so an override that has since stopped applying, such as one a later managed pin overrules, can still be removed. It refuses a key the task has no override of in effect.
- Each records the task's next override record, `op` `set` with the `value`, or `op` `unset`, numbered from 1 per task, sets and unsets alike, with the Guildmaster's `text`, author `director` and the time recorded, and prints its receipt, record `task-override/<n>`, mutation `task-override:<repo>:<task>:<op>:<key>:<prior>`, hashed as for `task new` when longer than 128 bytes, `prior` being how many records the task already has for that key. Running the same command again, while its record is still that key's latest, prints the original receipt and records nothing; setting `high`, then `low`, then `high` again records three records.
- `--text` must be non-blank UTF-8 of at most 64 KiB. A task that is unknown or not open and a record made meanwhile for the same key are refused with exit 1 and nothing is written; a missing `--key`, `--value` on `set` or `--text`, a flag the subcommand does not take, and a missing or extra task are usage errors (exit 2).
- `list` prints the task's override records as a JSON array in number order; it opens the store read-only.

For each key the latest record wins: the task's overrides in effect are the keys whose latest record is a `set`.
The store itself refuses an empty key, one with whitespace and a step key (`step.`), but it has no config, so only `set` judges a key against the registry.
Records are append-only: an `unset` is its own record, so the history stays whole.
Recording an override launches, re-runs and invalidates nothing.
Every advance of the task applies its overrides in effect, so each stage it starts afterwards reads the task's effective values ([where they take effect](configuration.md#setting-scopes)); a finished stage is never re-run, and its binding records what it was first given, while an attempt relaunched after a crash reads the overrides current at the relaunch.
`task new --override <key>=<value> --override-quote <text>` gives a new task its overrides as it is created, each checked first, so a refused one creates no task.
[`task show`](#commands) lists a task's overrides in effect and, given the copy's config, those it would skip and why; [`task timeline`](#inspection) lists every record; and [`director status`](director.md#status) marks a task that has any.
An override that no longer applies, such as one a managed pin now overrules, is skipped rather than failing the advance; each Artisan attempt's [binding](interpreter.md#rounds-and-loops) records the overrides applied and those skipped, with why.

## Design rounds

A design round asks for a design of a feature task before anyone builds it, for the Guildmaster to decide on.
The Director runs one when the Guildmaster, or the Director itself, wants a design before build; whether to run one, and how many, is the Director's call, and there is no cap.

```
guildhall task design --repo <id> --task <id> --brief <file|-> [--copy <dir> | --store <path> --config <file> --state <dir>]
```

The brief says what the design must show and why; `-` reads it from standard input, with the same rules as `--detail-file`.
A missing, unreadable or empty brief, or a bad flag, is a usage error, exit 2, before the store is opened.
The command records the brief as round `n`'s and advances the task under the task lock and the admission gates, printing where it waits, as `task start` does.
Running it again with an identical brief is a safe replay that records nothing while the round is open; once the round's decision is answered, the same brief asks for the next round.

It is refused with exit 1, recording nothing:

- for a task that is not a feature task, one whose workflow has no Director `grill` and `build` step, as every bug task's: "bugs have no design round";
- once any `build` attempt exists: "build has started";
- while a round is open, until its decision is answered, even for a request with another brief;
- while any attempt of the task runs;
- until the grill has succeeded, and again after a `changes_requested` reopened it, until it succeeds again;
- for a done or cancelled task.

Each accepted request is one round, whatever its outcome.
The round's designer Artisan, `designer-<k>`, runs under the shipped `designer` profile at effort `high`, given the brief and the latest grill notes, research notes and spec, and writes a design: the options, mock-ups as text or diagrams, and the trade-offs.
It waits for the task's [dependencies](#dependencies) and the pause hold, and counts against the attempt caps; a failed designer is retried with `step retry --step designer`, within the same round.
The design is then asked as the Guildmaster decision `design-<n>`, and `task start --decision design-<n>` moves the task on once it is answered:

- `approved`: plan is given the design; if plan already ran, it runs again with it and its acceptance is asked again on the new spec.
- `changes_requested`: the grill reopens, handed to the Director with the answer as `feedback`, its notes as `previous` and the design as `reviewed`; once it finishes, the Director may ask for another round.

When the task's workflow has a step named `design` or `designer`, the round's ids are prefixed with `round-` (`round-design-<n>`, `round-designer-<k>`).
`task show` and `task timeline` print `design_rounds`, the rounds used, once the task has had one.
See the [interpreter](interpreter.md#design-rounds) for how the round runs and replays.

## Task grants

A task grant gives one task's claude Artisans extra [allowed tools](adapters.md#artisan-permissions) for a while, instead of widening a profile in `config.toml` for every task ([decision](decisions/task-grants.md)).
The Director opens one only when the Guildmaster asks for it; the Guildmaster's answer to its decision approves it.

```
guildhall task grant create --store <path> --config <director.toml> --repo <id> --task <id> --step <id|all> --tool <rule> [--tool <rule>]... --reason <text> [--until attempt|done]
guildhall task grant revoke --store <path> --repo <id> --task <id> --grant <id> --reason <text>
guildhall task grant list   --store <path> [--repo <id>] [--task <id>]
```

`--copy <dir>`, or the copy found from the current directory, stands in for `--store` and `--config`.

- `create` records grant `g<n>`, numbered from 1 per task, for one step of the task's bound workflow whose profile runs the `claude` adapter, or `all` of them, and in the same transaction opens decision `grant-g<n>` of kind `grant` with the options `approve` and `reject`, whose question quotes the step, rules, reason and expiry. It prints the receipt, record `task-grant/g<n>`, mutation `task-grant:<repo>:<task>:g<n>`. `--until` is `attempt`, the default, which lasts for the first attempt of the step bound after approval until it has an outcome, or `done`, which lasts until the task is merged or closed; `--step all` needs `--until done`.
- `revoke` records the revoke once, mutation `task-grant-revoke:<repo>:<task>:<id>`, on a pending or an active grant. A pending grant's decision stays open, but approving it no longer activates anything.
- `list` prints every grant, one task's, one repository's or the store's, with its derived `state` and, once approved, `approved_at`; it opens the store read-only.

Each command prints one JSON document; exit codes are those of [`decide`](decisions-cli.md): 0, 1 for a refusal and 2 for a usage error.
Every rule is checked as a profile's `allowed_tools` are, a repeated rule is refused, and a task grant never allows a `Bash` rule for `guildhall`, `gh` or `glab`, or for `git` without a subcommand or with `push`, including a wildcard command that could complete to one, such as `Bash(g*)`, and one named after a wrapper, such as `Bash(env gh:*)`.
At most one grant per task and step is pending or active, `all` overlapping every step, and a second is refused naming the first; repeating the same `create` while its grant is pending or active prints the original receipt.
The workflow the repository's config gives now must be the task's bound revision, or `create` cannot check the step and refuses.

A grant's state is derived, never written: `pending` until its decision is answered, `rejected` when the answer is not `approve`, `revoked` once revoked, `expired` once its task is closed or its one bound attempt has an outcome, and otherwise `active`.
The engine records an active grant in the binding of each attempt it applies to, `<step>-<n>.inputs.json`, as `grant`, and the Claude wrapper gives its rules from there, so a revoke reaches the next wrapper run, never a running session; stop the attempt to cut it short.
Rebaser and conflict-resolution attempts never take a task grant.
`director status` lists the active grants in its [`grants`](director.md#status) section, and an open grant decision is in `needs_me` like every decision.
