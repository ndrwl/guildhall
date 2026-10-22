# Running a task

`guildhall task start`, `guildhall step done` and `guildhall step retry` drive a task through the [interpreter](interpreter.md) from the command line.
Each runs one `interp.Engine.Advance` and prints where the task now stands; launching an Artisan attempt runs it as its own [owned launcher](artisan.md#commands) process, which outlives the command.
When the advance runs from a [copy](#copies), each attempt's launcher is also the task's watcher: once it records the attempt's outcome it runs `step done` or `task start` itself ([the watcher](#the-watcher)), so nobody types `step done`.
Over explicit `--store`, `--config` and `--state` paths nothing advances in the background, and the caller runs `step done` once the result file exists.

```sh
guildhall task start --store <file> --config <director.toml> --state <dir> --repo <id> [--decision <id>] <task>
guildhall step done --store <file> --config <director.toml> --state <dir> --repo <id> --task <id> --step <id> --attempt <n> --result <file>
guildhall step retry --store <file> --config <director.toml> --state <dir> --repo <id> --task <id> --step <id> --attempt <n> [--reason <text>]
```

With a [copy](#copies), `--copy <dir> --repo <id>` replaces `--store`, `--config` and `--state`.

## Copies

A Director copy is one directory holding `config.toml`, an optional `managed.toml`, `director.store` and `state/` ([the pin](releases.md#the-pin)).
Every command that takes a store (`task`, `step`, `decide`, `messages`, `workflow`, the owned `artisan` commands and `store init|doctor`) accepts `--copy <dir>`, or, when that flag is absent, the environment variable `GUILDHALL_COPY`.
The copy defaults `--store` to `<dir>/director.store`, `--config` to `<dir>/config.toml` and `--state` to `<dir>/state`, for each of those flags the command takes; `workflow approve` takes the copy's config only with `--repo` and no `--file`.
`config.Load` reads the copy's config as it reads any other.

- `--copy` overrides `GUILDHALL_COPY`.
- `GUILDHALL_COPY` is only a default: a command given any of its path flags (`--store`, `--config`, `--state`, or the `store init|doctor` path) without `--copy` ignores it entirely, so a launched child given explicit paths keeps them, and its other path flags stay required.
- When `GUILDHALL_COPY` is used it must be an absolute path, so a child that inherits it in another working directory selects the same copy; a relative value is a usage error.
- A path flag given with `--copy` must name the copy's own file, compared absolute and cleaned (ignoring case on Windows); one elsewhere, such as a `--store` outside the copy, is a usage error, exit 2.
- With neither `--copy` nor `GUILDHALL_COPY`, the path flags are required as before.
- A copy, like a `--store`, without a store file is refused with exit 1, naming `guildhall store init <dir>/director.store`; no command but `store init` creates a store.

## What an advance runs over

| Input | Where it comes from |
|---|---|
| Records, outbox | The store at `--store`, opened for each record operation and closed before the next, so an attempt's launcher can take its ownership while the advance runs |
| Workflow | The repository's effective workflow, resolved from `--config` exactly as [`task new`](tasks.md) resolves it; a definition that is no longer the task's bound revision is refused |
| Profiles, merge policy | The Director config; a step without a profile runs the `fake` adapter, and a profile may name `fake` or `claude`, the [runtimes](adapters.md#running-an-attempt) `artisan start` runs owned |
| Standards revision | The config setting `standards_revision` |
| Stall interval | The config setting [`stall_interval`](configuration.md#settings), default 15 minutes, `0s` off: an advance that finds a launched attempt waiting sends the Director one [`worker-stalled`](interpreter.md#worker-stalled-observations) message when its heartbeat is fresh and its worker silent for longer |
| Forge | [`forge.GitHub`](contracts/forge.md): `git` pushes from the repository's `path` to its `remote`, and `gh`, found on `PATH` and signed in with its own credentials, opens and merges pull requests; only `github.com` remotes are supported |
| Baseline | The role `roles/artisan.md` and the guide `stages/<kind>.md` from `baseline/` beside the config file when there, else from the [release baseline](contracts/baseline.md) embedded in the binary, so a copy with only its config file runs every shipped Artisan kind; every `baseline/standards/*.md` in name order, because the release ships no standards; a step profile's `role` overrides the default role and is resolved relative to the config directory; a guide in neither root is refused, as is a copy file that exists but cannot be read, and the manifest records each file's root, `copy` or `release` |
| Skills | The embedded release skills, installed into a temporary root for the advance |
| Attempts | `<state>/<repository>/<task>/<step>-<n>`, laid out by `artisan.AttemptKey` |
| Working directory | `<state>/<repository>/<task>/work`, a [git worktree](interpreter.md#the-working-directory) of the repository's `path` on branch `guildhall/<task>`, which the first attempt creates from the default branch of the repository's `remote`, fetched with `git` found on `PATH`, and every later attempt reuses, and the merge that closes the task removes with its local branch when nothing in them is undelivered; the `path` must be absolute on the machine the Director runs on |
| Documents | `<state>/documents` |

An advance holds the task's lock, `attempt.lock` in `<state>/<repository>/<task>`, for its whole run, waiting at most 30 seconds for it, so two advances of one task never interleave; the interpreter is not safe for concurrent use on one task.
Each `git` or `gh` run of the forge and of the working directory is bounded by five minutes, so a forge that does not answer is an error that records nothing, and an interrupt (Ctrl+C) ends the advance; either way the lock is released and running the command again replays the advance.
The interrupt is exercised by a Unix test only; a second interrupt while the advance unwinds ends the process at once.

## The watcher

An advance from a copy launches each attempt with `artisan start --copy <dir> --advance` ([commands](artisan.md#commands)).
Once the launcher has published the attempt's outcome, recorded its store outcome and released its ownership, it advances the task over the same copy:

| How the attempt ended | The launcher runs |
|---|---|
| Finished with a valid result, succeeded or failed | `guildhall step done --copy <dir> --repo <repo> --task <task> --step <step> --attempt <n> --result <attempt dir>/result.json` |
| Anything else: failed without a result, stopped, or failed at setup | `guildhall task start --copy <dir> --repo <repo> <task>`, which sends the Director `attempt-failed` |

The advance's output is appended to the attempt's `launcher.log` and an `advance` event with its exit status to its `events.jsonl` ([events](artisan.md#events)).
A failed advance changes neither the attempt's outcome nor the launcher's exit status: both commands are idempotent, so the Director's next `task start` repeats what was lost ([recovery](interpreter.md#replay-and-recovery)).
An attempt [`artisan resume`](artisan.md#resume) settles after its launcher died is not advanced; the Director's `task start` after the resume is.

## `task start`

`task start` advances the task from its records with the `task-created` event: an unstarted task launches its first ready stage, and a task still waiting on a running attempt or unanswered decision changes nothing.
It can also recover intake for a launcher-completed successful attempt and continue the workflow; see [recovery](interpreter.md#replay-and-recovery).
With `--decision <id>` it feeds `decision-answered` for that answered decision instead, which is how a task continues after [`decide answer`](decisions-cli.md) (answering never advances a task by itself).

## `step done`

`step done` is the result intake for one attempt.
`--result` must be the attempt's own result file, `result.json` in its attempt directory; any other path is refused, because the interpreter reads results only there.
The file is validated with `adapter.ReadResult` for identity `<task>`, `<step>-<n>` before the engine is called, so a missing, malformed or other attempt's result changes nothing.
It then feeds `attempt-result`: a succeeded result's outputs are recorded as documents and the task moves on.

The attempt's launcher records its store outcome when it decides the attempt, which may be before or after `step done`.
The [interpreter's recovery contract](interpreter.md#replay-and-recovery) defines intake replay and outcome reconciliation when these operations overlap.

## `step retry`

`step retry` is the Director's decision after a failed or stopped attempt, which leaves the task waiting with `{"wait":"director",...}`.
It feeds [`attempt-retried`](interpreter.md#events) for attempt `--attempt` of `--step`: valid only when that attempt is the step's latest and finished `failed` or `stopped`, and the task is open.
The engine then starts the next attempt exactly as `task start` starts a first one, and prints `{"wait":"attempt",...}` for it.
`--reason`, when given, is recorded as the Director message `<repo>.<task>.attempt-retried.<step>-<n>` once the next attempt has launched, so `task timeline` shows why the attempt was retried.
A retry whose start or launch fails exits 1 and sends no reason, so it can be repeated with another reason; when the next attempt was recorded but not launched, the repeat launches it.
A running or succeeded attempt, or one a later attempt already follows, is refused with exit status 1 and nothing is recorded; repeating the command after the next attempt launched prints what the task waits for and writes nothing beyond the reason, if an interrupted retry had not yet sent it.
An attempt whose launcher died is first settled with [`artisan resume`](artisan.md#commands).

## Output and exit status

All three print one JSON line, what the task waits for, and write the same line to `next.json` beside the task lock, `<state>/<repository>/<task>/next.json`, before releasing the lock, which [`director status`](director.md#status) reports: `{"wait":"attempt","step":"plan","attempt":1}`, `{"wait":"decision","step":"accept","decision":"accept-1"}`, `{"wait":"director","step":"build","attempt":2}` (or, after a held or refused merge, `{"wait":"director","step":"deliver","decision":"deliver-1"}`), `{"wait":"checks","step":"validate"}` while the published head's [checks](interpreter.md#validate) are pending or a failed head waits for a re-run, which a later `task start` reads again once `checks_refresh` has passed, or, once the merge landed, `{"wait":"done","step":"deliver"}`.

| Exit status | Meaning |
|---|---|
| 0 | The advance completed |
| 1 | The configuration, workflow, store, result or engine refused or failed; the error is on standard error |
| 2 | Usage error |
