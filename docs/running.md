# Running a task

`guildhall task start`, `guildhall step done`, `guildhall step finish`, `guildhall step retry` and `guildhall step send-back` drive a task through the [interpreter](interpreter.md) from the command line.
Each runs one `interp.Engine.Advance` and prints where the task now stands; launching an Artisan attempt runs it as its own [owned launcher](artisan.md#commands) process, which outlives the command.
When the advance runs from a [copy](#copies), each attempt's launcher is also the task's watcher: once it records the attempt's outcome it runs `step done` or `task start` itself ([the watcher](#the-watcher)), so nobody types `step done`.
Over explicit `--store`, `--config` and `--state` paths nothing advances in the background, and the caller runs `step done` once the result file exists.

```sh
guildhall task start --store <file> --config <director.toml> --state <dir> --repo <id> [--decision <id>] [--ignore-deps --reason <text>] <task>
guildhall step done --store <file> --config <director.toml> --state <dir> --repo <id> --task <id> --step <id> --attempt <n> --result <file>
guildhall step finish --store <file> --config <director.toml> --state <dir> --repo <id> --task <id> --step <id> --attempt <n> --summary <text> (--output <port>=<file>... | --failed)
guildhall step retry --store <file> --config <director.toml> --state <dir> --repo <id> --task <id> --step <id> --attempt <n> [--reason <text>]
```

With a [copy](#copies), `--copy <dir> --repo <id>` replaces `--store`, `--config` and `--state`.

## Copies

A Director copy is one directory holding its [copy marker](#the-copy-marker) `.guildhall-copy`, `config.toml`, an optional `managed.toml`, `director.store` and `state/` ([the pin](releases.md#the-pin)), and, once a session has run, `director/session-<n>/` for each [Director session](director.md#start).
Every command that takes a store (`task`, `step`, `decide`, `messages`, `workflow`, `director`, `spend`, `quality`, `version`, the owned `artisan` commands and `store init|doctor`) selects its copy with `--copy <dir>`, or, when that flag is absent, by **copy discovery** from the current directory.
The copy defaults `--store` to `<dir>/director.store`, `--config` to `<dir>/config.toml` and `--state` to `<dir>/state`, for each of those flags the command takes; `workflow approve` takes the copy's config only with `--repo` and no `--file`.
`config.Load` reads the copy's config as it reads any other.

- `--copy` comes first, then copy discovery ([decision](decisions/copy-discovery.md)).
- Copy discovery starts from the cleaned absolute current directory and walks up one parent at a time to the filesystem root (on Windows, the drive root), stopping at the first directory that holds `.guildhall-copy`: that directory is the copy, so a command run anywhere inside a copy, such as in a session's `director/claude` workspace, finds it.
- A start at or under that copy's `state/` finds no copy, and the search does not go on past it: every Artisan working directory and attempt directory lies under `<copy>/state/`, so nothing an Artisan runs can reach the Director's store. The paths are compared absolute and cleaned, ignoring case on Windows.
- Explicit path flags bypass copy selection: a command given any of its path flags (`--store`, `--config`, `--state`, or the `store init|doctor` path) without `--copy` does no discovery, so a launched child given explicit paths keeps them, and its other path flags stay required.
- The environment never selects a copy.
- A path flag given with `--copy` must name the copy's own file, compared absolute and cleaned (ignoring case on Windows); one elsewhere, such as a `--store` outside the copy, is a usage error, exit 2.
- With no `--copy`, no path flags and no copy found, the command refuses with one line, `no Guildhall copy found from <cwd>; pass --copy <dir> or cd into a copy`, after its command name, with the exit status it gave for a missing copy before.
- A copy, like a `--store`, without a store file is refused with exit 1, naming `guildhall store init <dir>/director.store`; no command but `store init` creates a store.

### The copy marker

The copy marker is the file `.guildhall-copy` at the copy root, one JSON object: `{"schema":"guildhall.copy","version":1,"name":"<copy name>"}`.
A command run with `--copy <dir>` writes it, named for the directory's base name, when `<dir>` has none, so `store init --copy <dir>` gives a new copy its marker and every existing copy gets one at its first use with `--copy`; a bare `store init <path>` writes none, since it cannot tell it is making a copy.
The marker is created exclusively, so concurrent first uses cannot race, and an existing marker is never rewritten.
A copy that cannot be written, such as a read-only one, gets one warning line on standard error and the command goes on.
Discovery refuses a marker that cannot be read, is not JSON or has an unknown schema or version, with an error naming the file; it does not skip it to look further up.

## What an advance runs over

| Input | Where it comes from |
|---|---|
| Records, outbox | The store at `--store`, opened for each record operation and closed before the next, so an attempt's launcher can take its ownership while the advance runs |
| Workflow | The task's bound workflow, resolved from `--config` by the bound revision's name as [`task new`](tasks.md#the-bound-workflow) resolves a name, with the repository's current overrides, whatever the repository's `workflow` setting now says; a definition whose digest is no longer the bound one, because its file or the overrides changed, is refused naming the bound workflow, while a task bound when role profiles were written into the digest still resolves to that legacy digest ([decision](decisions/role-profiles-outside-the-digest.md)), and a task bound to an earlier release's shipped `default` or `bug` still resolves to that retired revision ([decision](decisions/retired-shipped-revisions.md)) |
| Profiles, merge requirements | The Director config, with the [shipped role profiles](configuration.md#profiles) it does not declare; an Artisan step runs under the profile it names or else its role profile, and one whose type has no role and that names none is refused by `task new` and advance by step name, directing you to set a profile on that step; explicit `fake` and `claude` profiles remain available, the [runtimes](adapters.md#running-an-attempt) `artisan start` runs owned |
| Standards revision | The config setting `standards_revision` |
| Guard rails | The config's [guard rails](director.md#guard-rails): a repository the config does not list is refused before anything else is read or written, and every new attempt starts under `max_running_attempts` and `max_attempts_per_day`: one the running cap does not admit is queued, exit 0 with `wait: queued` and its place, and started by the session supervisor when a slot frees, and one past the daily cap is refused with exit 1 naming the rail and a `budget-refused` message to the Director |
| Stall interval | The config setting [`stall_interval`](configuration.md#settings), default 15 minutes, `0s` off: the attempt's launcher, given it as `--stall-interval`, sends the Director one [`worker-stalled`](interpreter.md#worker-stalled-observations) message from its heartbeat when its worker has been silent for longer, and an advance that finds a launched attempt waiting sends the same message, a replay when the launcher sent it, when its heartbeat is fresh and its worker silent for longer |
| Forge | [`forge.GitHub`](contracts/forge.md): `git` pushes from the repository's `path` to its `remote`, and `gh`, found on `PATH` and signed in with its own credentials, opens and merges pull requests; only `github.com` remotes are supported |
| Baseline | The role `roles/artisan.md`, or for a [Director stage](interpreter.md#director-stages) `roles/director.md` unless the config's `[director] role` names another, and the guide `stages/<kind>.md` from `baseline/` beside the config file when there, else from the [release baseline](contracts/baseline.md) embedded in the binary, so a copy with only its config file runs every shipped Artisan kind; every `baseline/standards/*.md` in name order, because the release ships no standards; a step profile's `role` overrides the default role and is resolved relative to the config directory; a guide in neither root is refused, as is a copy file that exists but cannot be read, and the manifest records each file's root, `copy` or `release` |
| Skills | The embedded release skills, installed into a temporary root for the advance |
| Attempts | `<state>/<repository>/<task>/<step>-<n>`, laid out by `artisan.AttemptKey` |
| Working directory | `<state>/<repository>/<task>/work`, a [git worktree](interpreter.md#the-working-directory) of the repository's `path` on branch `guildhall/<task>`, which the first attempt creates from the default branch of the repository's `remote`, fetched with `git` found on `PATH`, and every later attempt reuses, and the merge that closes the task removes with its local branch when nothing in them is undelivered; the `path` must be absolute on the machine the Director runs on |
| Base and classifier | For an Artisan code review, the [rebase-aware review](interpreter.md#rebase-aware-review)'s `Engine.Base` is the merge base of branch `guildhall/<task>` and the remote's default branch as the working directory's custody last fetched it (`refs/guildhall/default/guildhall/<task>`), read from the repository's `path` with no network call, and its classifier is `git.Program` over that `path`; a base that cannot be read, such as with no working directory yet, leaves the review full |
| Documents | `<state>/documents` |

An advance holds the task's lock, `attempt.lock` in `<state>/<repository>/<task>`, for its whole run, waiting at most the copy's [`lock_wait`](configuration.md#settings), 30 seconds by default, for it, so two advances of one task never interleave; the interpreter is not safe for concurrent use on one task.
The wait applies to store opens as specified in [settings](configuration.md#settings).
Commands that do not advance retain their existing lock-wait defaults.
Each `git` or `gh` run of the forge, of the working directory and of the review classifier is bounded by five minutes, so a forge that does not answer is an error that records nothing, and an interrupt (Ctrl+C) ends the advance; either way the lock is released and running the command again replays the advance.
The interrupt is exercised by a Unix test only; a second interrupt while the advance unwinds ends the process at once.

## The watcher

An advance from a copy launches each attempt with `artisan start --copy <dir> --advance` ([commands](artisan.md#commands)).
Once the launcher has published the attempt's outcome, recorded its store outcome and released its ownership, it advances the task over the same copy:

| How the attempt ended | The launcher runs |
|---|---|
| Finished with a valid result, succeeded or failed | `guildhall step done --copy <dir> --repo <repo> --task <task> --step <step> --attempt <n> --result <attempt dir>/result.json` |
| Anything else: failed without a result, stopped, or failed at setup | `guildhall task start --copy <dir> --repo <repo> <task>`, which sends the Director `attempt-failed` |
| Any end of a delivered task's [judge](quality.md#running-it) attempt, step `quality-judge` | `guildhall quality judged --copy <dir> --repo <repo> --task <task> --attempt <n>`, because the task is done |

The advance's output is appended to the attempt's `launcher.log` and an `advance` event with its exit status to its `events.jsonl` ([events](artisan.md#events)).
An advance that exits unsuccessfully, such as one that waited its `lock_wait` for a task lock another advance held, is run once more, which waits the lock wait again.
When that run fails too, the launcher sends the Director `<repo>.<task>.advance-failed.<step>-<n>`, type `advance-failed`, naming the command and its last line of error output; the send goes through the store with no task lock, and the Director is [woken](messaging.md#waking-the-director) for it as for any other send.
[`director status`](director.md#status) lists the attempt nobody took as an `advance` item until an advance takes it.
An operational advance failure does not reclassify the attempt or change the launcher's exit status: both commands are idempotent, so the Director's next `task start`, or the `step done` the status item names, repeats what was lost ([recovery](interpreter.md#replay-and-recovery)).
A result rejected during intake instead records a failed attempt with its reason and waits for the Director, even when the launcher recorded success; a later notification or persistence error may still require replay to finish that advance.
An attempt [`artisan resume`](artisan.md#resume) settles after its launcher died is not advanced; the Director's `task start` after the resume is.

## `task start`

`task start` advances the task from its records with the `task-created` event: an unstarted task launches its first ready stage, and a task still waiting on a running attempt or unanswered decision changes nothing.
It can also recover intake for a launcher-completed successful attempt and continue the workflow; see [recovery](interpreter.md#replay-and-recovery).
With `--decision <id>` it feeds `decision-answered` for that answered decision instead, which is how a task continues after [`decide answer`](decisions-cli.md) (answering never advances a task by itself).

A task whose [dependencies](tasks.md#dependencies) have not all merged waits at the [dependency gate](interpreter.md#the-dependency-gate) after its grill and prints `{"wait":"dependencies","step":"<step>"}`.
`--ignore-deps --reason <text>` starts it anyway: before advancing, the command records the Director's dependency override, with the reason, under mutation id `dependency-override.<repo>.<task>`, then advances as usual, so the task passes the gate, its branch is fast-forwarded to the current main, and it is never held again.
The override shows in [`task timeline`](tasks.md#inspection) as `dependency_override`, with its `reason` and `overridden_at`, and as an `override-dependencies` event.
Repeating the command with the same reason replays the override; there is no engine event for it.
The command refuses, recording nothing: `--ignore-deps` without `--reason` or with an empty one, `--reason` without `--ignore-deps` and a reason over 64 KiB, each a usage error (exit 2); a task that is done or cancelled, a task with no `depends_on`, a second override with another reason ("already overridden"), and new work while the Director session is [recovering](director.md#ready), each exit 1.

## `step done`

`step done` is the result intake for one attempt.
`--result` must be the attempt's own result file, `result.json` in its attempt directory; any other path is refused, because the interpreter reads results only there.
The file is read with `adapter.ReadResult` for identity `<task>`, `<step>-<n>` before the engine is called; missing or unreadable files and unsupported newer versions remain operational errors.
Malformed or wrong-identity result bytes are passed to the engine for durable rejection under the task lock.
It feeds `attempt-result`: a valid succeeded result's outputs are recorded as documents and the task moves on, while rejected results or outputs record a failed attempt with the reason and wait for the Director.

The attempt's launcher records its store outcome when it decides the attempt, which may be before or after `step done`.
The [interpreter's recovery contract](interpreter.md#replay-and-recovery) defines intake replay and outcome reconciliation when these operations overlap.

## `step finish`

`step finish` is the Director's verb for a [Director stage](interpreter.md#director-stages), which leaves the task waiting with `{"wait":"director-stage",...}` and sends the Director a `director-stage` message naming the stage's prompt.
It ends attempt `--attempt` of `--step` with the Director's `--summary`, either succeeded with one `--output <port>=<file>` for each output port the step's kind declares (a grill's is `notes`), or abandoned with `--failed` and no output.
Under the task's lock it copies each file into the task's working directory as `<port><ext>`, `<ext>` being the file's extension, so `--output notes=agreed.md` becomes `work/notes.md`, then writes the attempt's `result.json` with `adapter.WriteResult`, which never replaces a result, and feeds `attempt-result` exactly as `step done` does: the notes are recorded as `<step>-<n>-notes` and the task moves on.
A `--failed` finish leaves the task waiting for the Director, and [`step retry`](#step-retry) hands the stage's next attempt to it.

It is refused with exit status 1, copying and writing nothing, when the step is not in the repository's workflow or not run by the `director`, when an output names a port the step's kind does not declare or a file that cannot be read, when the attempt is not recorded or a later attempt of the step follows it, when it is already finished (its outcome recorded or its result written; a written result not yet taken is taken with `step done` or `task start`, and [`director status`](director.md#status) lists it as an `advance` item), and when the engine has not yet handed it to the Director (`task start` hands it).
A missing `--summary`, an output that is not `<port>=<file>`, a port named twice, outputs with `--failed` or none without it are usage errors.

## `step retry`

`step retry` is the Director's decision after a failed or stopped attempt, which leaves the task waiting with `{"wait":"director",...}`.
This includes a launcher-succeeded attempt whose result intake was rejected: run `task start` or `step done` first to apply the rejection if an earlier release left it succeeded with unfinished intake.
The failure's `intake_rejection`, the `attempt-failed` message and the status feed's retry reason explain why it was rejected; no hand-written replacement verdict is needed.
It feeds [`attempt-retried`](interpreter.md#events) for attempt `--attempt` of `--step`: valid only when that attempt is the step's latest and finished `failed` or `stopped`, and the task is open.
The engine then starts the next attempt exactly as `task start` starts a first one, and prints `{"wait":"attempt",...}` for it.
The next attempt's prompt opens with a "Why this is a retry" section quoting `--reason` (or "None given."), the retried attempt's intake rejection when it had one, and its result summary (or "None recorded.", as for a stopped attempt); the section is recorded in the next attempt's binding, so a resumed advance renders it too.
`--reason`, when given, is also recorded as the Director message `<repo>.<task>.attempt-retried.<step>-<n>` once the next attempt has launched, so `task timeline` shows why the attempt was retried.
A retry whose start or launch fails exits 1 and sends no reason message, so it can be repeated; when the next attempt was recorded but not launched, the repeat launches it.
A retry over the running cap is [queued](director.md#the-start-queue): it prints `{"wait":"queued","step":...,"attempt":<n>,"position":<p>}`, exit 0, and the session supervisor starts it when a slot frees, sending the reason then; a retry naming another attempt while one is queued is refused with exit 1.
A retry queued or refused once the next attempt's inputs were bound, such as by a guard rail or a failed launch, keeps the first reason: a repeat with another reason renders and sends the first, as a send-back's feedback is fixed once recorded.
A running or succeeded attempt, or one a later attempt already follows, is refused with exit status 1 and nothing is recorded; repeating the command after the next attempt launched prints what the task waits for and writes nothing beyond the reason, if an interrupted retry had not yet sent it.
An attempt whose launcher died is first settled with [`artisan resume`](artisan.md#commands).

## `step park` and `step unpark`

```sh
guildhall step park --repo <id> --task <id> --step <id> --attempt <n> --reason <text> [--until-merged <repo>/<task>]... [--copy <dir> | --store <path> --config <file> --state <dir>]
guildhall step unpark --repo <id> --task <id> --step <id> --attempt <n> [--copy <dir> | --store <path> --config <file> --state <dir>]
```

`step park` is the Director's decision not to retry a failed or stopped attempt yet, with its `--reason`, such as waiting for another task to merge ([decision](decisions/0092-park-attempt.md)).
While the attempt is parked, [`director status`](director.md#status) leaves its `retry` item, and the task's `director` item for that attempt, out of `needs_me` and lists it under `parked` instead, so it no longer wakes [`director resume`](director.md#pause-and-resume), counts as Lanterns attention or appears as work in a new session's startup instructions.
Each command prints the attempt's park record as one JSON line, `{"repository":...,"task_id":...,"step":...,"attempt":<n>,"reason":...,"parked":<bool>,"cycle":<n>}`.

Parking is a record only: it takes no task lock, writes no attempt outcome and no `next.json`, opens or answers no decision, sends no message and starts nothing, so `task start` of a parked task still prints `{"wait":"director",...}`.
It does not acknowledge the attempt's `attempt-failed` message; acknowledge that separately.
It is refused with exit status 1, writing nothing, for a task that is not open, the `quality-judge` step, an attempt that is not the step's latest, and an attempt that is running or succeeded; an unknown task or attempt is also exit 1.
Repeating an identical park prints the record and writes nothing; a park with another reason while the attempt is parked is refused with `step park: attempt <n> of <step> of <repo>/<task> is already parked, with reason "<prior>"; unpark it first`.

`--until-merged <repo>/<task>`, which may be repeated, holds the park only until every named task merges ([decision](decisions/0096-hold-until-merged.md)); the record gains `"until_merged":[{"repository":...,"id":...},...]`.
Each named task must be a task of this store that is still open and not the parked attempt's own task: one already done is refused with `<repo>/<task> already merged; act now instead`, and one cancelled likewise, so the Director acts at once.
While any named task is open the park counts exactly as one without the flag, and its `parked` entry gains `until_merged` and `waiting_for`, the named tasks not merged yet.
Once all have merged, or as soon as one is cancelled, the park stops counting with no write: the `retry` item is back in `needs_me`, its reason ending `; released: <repo>/<task> merged <time>` or `; dependency cancelled: <repo>/<task>`, then `; park reason: <reason>`, and the session supervisor wakes the Director once with a `hold-released` message ([holds](director.md#holds)).
Nothing is retried automatically.
A park while already parked with another `--until-merged` set, or without one, is refused as another reason is; `step unpark` lifts a held park early, and `step retry` clears it as it clears any park.

`step unpark` lifts the park, so the attempt's `retry` and `director` items are back in `needs_me` exactly as before; an attempt that is not parked is refused with exit status 1.
A later park of the same attempt records a new park.

[`step retry`](#step-retry) of a parked attempt works as it always does, and the park stops counting once attempt `n+1` is recorded, with nothing to unpark; a retry refused before it records the next attempt leaves the park in force.
A `paused` item is not affected by a park: a retry the Director asked for while [paused](director.md#pause-and-resume) wins over it.
Both commands refuse a repository the config does not list, as every command that acts on one does.

## `step send-back`

[`step send-back`](step-send-back.md) starts a new attempt of a succeeded Artisan step with required `--feedback` from the Director.
See the command guide for admission requirements, prepared-input refusals, replay semantics and downstream review behaviour.
This is one option after a `pr-blocked` or `rebase-stuck` report, when Guildhall could not [rebase the pull request itself](interpreter.md#tiered-rebase); holding the task or leaving it unadvanced are alternatives.

## `task design`

`guildhall task design --repo <id> --task <id> --brief <file|->` adds a [design round](tasks.md#design-rounds) to a feature task after its grill and before its first build: it feeds `design-requested` with the brief, under the same flags, task lock and admission gates as `task start`, and prints where the task waits.
The round's designer is an Artisan attempt, so it waits for the task's dependencies, the pause hold and the attempt caps as any attempt does, and prints `{"wait":"attempt","step":"designer","attempt":<k>}` once launched; its design is then asked as the decision `design-<n>`, answered with `decide answer` and fed with `task start --decision design-<n>`.
A missing or empty brief and bad flags exit 2 before the store is opened; the [refusals](tasks.md#design-rounds), such as a bug task or a started build, exit 1 and record nothing.

## `ci log`

[`ci log`](ci-logs.md) prints the full log of a failed CI job that one of the task's validate results records, from the CI log cache or fetched with Guildhall's own gh; it takes no task lock and changes nothing.

## Output and exit status

All five print one JSON line, what the task waits for, and write the same line to `next.json` beside the task lock, `<state>/<repository>/<task>/next.json`, before releasing the lock, which [`director status`](director.md#status) reports: `{"wait":"attempt","step":"plan","attempt":1}`, `{"wait":"director-stage","step":"grill","attempt":1}`, `{"wait":"decision","step":"accept","decision":"accept-1"}`, `{"wait":"director","step":"build","attempt":2}` (or, after a held or refused merge, `{"wait":"director","step":"deliver","decision":"deliver-1"}`), `{"wait":"checks","step":"validate"}` while the published head's [checks](interpreter.md#validate) are pending or a failed head waits for a re-run, which a later `task start`, or a Director session's [checks watcher](director.md#checks), reads again once `checks_refresh` has passed, `{"wait":"dependencies","step":"research"}` while the [dependency gate](interpreter.md#the-dependency-gate) holds the task, which a Director session's [dependency watcher](director.md#dependencies) advances again, `{"wait":"merge-queue","step":"deliver"}` while an authorized merge waits in its repository's [merge queue](interpreter.md#the-merge-queue) or is held, which a Director session's [merge-queue watcher](director.md#merge-queue) advances again, or, once the merge landed, `{"wait":"done","step":"deliver"}`.
A behind or conflicting PR is [rebased by Guildhall](interpreter.md#tiered-rebase): at the front of the queue, or at validate when it conflicts.
Only a front that rebased three times in one stay, or a copy that cannot rebase, waits with `{"wait":"director","step":"validate"}` (or `deliver`) and sends one `pr-blocked` or `rebase-stuck` report per head and state, with URL, head commit, base tip, paths, behind count and options.
The Director chooses what to do; Guildhall does not keep polling the task after that wait.

While the Director is [paused or stopping](director.md#pause-and-resume), an advance that would start a new attempt, `task start` and `step retry` among them, starts none and waits `{"wait":"paused","step":"<step>"}`, the step whose attempt was held, which `next.json` records too.

| Exit status | Meaning |
|---|---|
| 0 | The advance completed |
| 1 | The configuration, workflow, store, result or engine refused or failed; the error is on standard error |
| 2 | Usage error |

## Pruning

`guildhall prune [--dry-run] [--repo <name>] [--task <id>] [--copy <dir>]` reclaims the disk space of `done` and `cancelled` tasks closed longer ago than [`prune_after`](configuration.md#settings), 14 days by default.
It takes `--copy <dir>`, or the copy found from the current directory, and reads the copy's `config.toml`, `director.store`, `state` and `quality` directories.
A task's close time is the commit time of its `task-state` receipt, or for a [cancelled](tasks.md#task-cancel) task its `cancel-task` receipt, which set its `cancelled_at`.

A task is prunable only when all of these hold, and is otherwise listed as skipped with the reason:

- it is `done` or `cancelled` and its close is at least `prune_after` ago (`too recent`);
- no `open` task names it in `depends_on` (`open dependant`);
- it has no open `audit` decision, one neither answered nor void (`audit ... open`); no close opens one any more, but one an earlier release opened still holds its task until answered ([decision](decisions/retire-quality-audit.md));
- no `self-review-<n>.json` under `<copy>/quality/<repo>/<task>/` has a criterion still pending its judge, or cannot be read (`self-review ... pending`);
- it has no unfinished attempt, judge attempts included, and no unreleased ownership record;
- its repository is in the config, whose `path` the worktree belongs to;
- the store does not record it as pruned completely, unless its CI log cache, `<state>/<repo>/<task>/ci-logs/`, exists again, fetched since or left by a prune from before the cache was pruned; a task pruned only in part is selected again.

`--task` names one task; the same rules apply to it, and an open task, or a completely pruned one with no CI log cache, named by `--task` is listed with that as its reason.
`--dry-run` and the real run share this selection, and a dry run removes nothing, takes no lock and writes nothing.

For each prunable task, while holding the task's lock (`attempt.lock` in `<state>/<repository>/<task>`, waiting at most `lock_wait`), and after checking the selection again under it, a prune:

- Retires the worktree again with `git.Program.Retire`, at the head the task's work is safe at (`interp.SafeHead`): the head its newest `<id>-merge` document names, newest meaning last in the store's document name order (so `deliver-10-merge` sorts before `deliver-2-merge`, and `outside-*` after `deliver-*`; a task normally has one), or for a task whose workflow has no deliver step, such as a hallmark, the head its [`finished`](interpreter.md#report-only-workflows) document names, and with the task's recorded documents as its outputs, as the merge did: it removes the worktree, the `guildhall/<task>` branch and the `refs/guildhall/default/…` ref only when nothing would be lost. A worktree Retire keeps is left untouched and the task is listed as skipped with `worktree kept: <reason>`; a cancelled task has no merge record, so its worktree and branch are always kept, with the reason that it was cancelled unmerged, and the Director decides what happens to them; a `work` directory that is not a worktree, from before worktrees, is listed with `not a worktree` and left untouched. A worktree is never removed any other way. A newest merge record, or a `finished` document, that is missing, does not match its recorded digest, does not decode, has another schema or names no valid head fails that task's prune with an error naming it, and its worktree is left untouched.
- Removes everything in each attempt directory (`<step>-<n>/`) but `result.json`, its `usage.json`, `outcome.json`, `prompt/manifest.json` and the attempt's `attempt.lock`, so [`spend report`](spend.md), the quality judge's builder lookup and `artisan status` read the same as before. An attempt with `pane-launch.json` and no `pane-closed.json`, its Herdr workspace not closed yet, keeps its pane files, so the [workspace close sweep](artisan.md#workflow-artisan-workspaces) can still close it, and the report says so.
- Removes the task's [CI log cache](ci-logs.md), `ci-logs/`, a cancelled task's too, counting its bytes in the space freed; [`ci log`](ci-logs.md) fetches a log it removed again. A task that keeps its worktree is listed with the size of the attempt and CI log files freed.
- Keeps the task's `attempt.lock`, the task directory, every store record and every recorded document under `<state>/documents`.

A file that cannot be removed, such as one held open on Windows, makes the prune partial: the output says what remains, the task is not recorded as complete and the next run tries it again.
Once removal has finished, the prune is recorded in the store as the task's [prune record](store.md#pruned-tasks); a crash before that leaves the task selectable and the rerun is idempotent, and a rerun that finds nothing new writes nothing.
A catch-up prune of a completely pruned task's CI log cache is recorded as the next run, with the cache's bytes.
`task files` then shows `pruned on <date>` ([inspection](tasks.md#inspection)).

The output lists each task pruned (or with `--dry-run`, that would be) with the space freed, each task skipped with its reason, and a total; a dry run counts a work directory with a `.git` file as a worktree Retire removes, so only the real run can find a worktree it keeps.
It exits 0 when the prune ran, a partial prune included, 1 when the config or store cannot be read or a removal step failed, and 2 on a usage error.

With [`prune_daily`](configuration.md#settings) `"true"`, the session supervisor runs the same prune, off its decision path, at session start when the copy's last prune, stamped in `<state>/prune-last.json`, was a day ago or more, and then once a day; its output goes to the supervisor's standard error.
For a task whose worktree it leaves it sends the Director one unread `prune-workspace-kept` message per task, not one a day, which does not wake it.
Claude Code's own transcripts, `director/session-<n>/` directories and caches outside the task directory are not pruned.
