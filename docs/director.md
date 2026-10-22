# The Director

The Director is the agent that runs a Director copy: it gives Artisans work, answers their messages and asks the Guildmaster for decisions ([architecture](architecture.md)).
This page describes the commands that serve the Director itself rather than one task.
`director status`, the [session record](store.md#director-sessions), the session supervisor `director start --foreground` that writes it, `director ready`, `director pause`, `director resume`, `director stop` and the standalone [guard rails](#guard-rails) are built; the session's Herdr tab and the handoff and reload commands are designed in the slice 4 plan and land later.
A message to the Director [wakes it](messaging.md#waking-the-director) when its session is running in a pane Guildhall created.

## Start

```sh
guildhall director start [runtime] --foreground [--check] [--copy <dir>]
```

`director start` is the session supervisor: it runs one Director session in the current terminal and records it ([decision](decisions/director-start.md)).
It follows the [role grammar](decisions/0053-cli-role-grammar.md): the runtime is the positional argument, else the config's [`[director] adapter`](configuration.md#the-director-table), and a copy without either is refused with exit 1 listing the adapters.
It needs a [copy](running.md#copies), from `--copy` or `GUILDHALL_COPY`, because the session lives in it.
`--foreground` is required: the session's own Herdr tab is designed and lands later, so without it `start` is a usage error, exit 2.

It first runs a preflight and prints one line per check, as `doctor` prints, stopping at the first that fails:

- `config`: the copy's `config.toml` is a Director config.
  Until managed mode lands, a copy with `managed.toml` beside its config, or with inline `[managed]` settings, is refused naming `guildhall director enrol`.
- `runtime`: the runtime is one of the adapters, and the program it runs is on `PATH`; the fake runtime is the `guildhall` executable itself.
- `store`: the copy's `director.store` exists and opens; a missing one is refused naming `guildhall store init`.
- `skills`: the release skills and baseline install.

A failed check is exit 1 with nothing recorded and nothing written in the copy, and `--check` exits 0 after the preflight, also recording nothing.

Then it records session `<n>`, one more than the newest, as `recovering` with its own process identity, release and config digest, which supersedes any earlier session still recorded live.
It renders the session into `<copy>/director/session-<n>/` with [`render.Session`](rendering.md#director-sessions):

- `role.md`: the role prompt, from `[director] role` relative to the config directory, else the copy's `baseline/roles/director.md`, else the release's;
- every release skill, byte for byte, in the runtime's skills directory, `.claude/skills/` for Claude Code;
- the runtime's own files: for Claude Code, `.claude/settings.json`, which lets Bash run `guildhall` and read-only `git` commands without asking, denies any `git` command with `--output` (which writes a file), and denies edits to the copy's `config.toml` and `managed.toml`;
- `startup.md`: the startup instructions, saying the session is starting up and recovering, naming the newest handoff note (none until handoff notes land), and listing the in-flight snapshot, each `needs_me` item of the [status feed](#status) with its command;
- `manifest.json`: the digest of every file above.

It then runs the runtime's session command as its child in that directory, with `GUILDHALL_COPY` set to the copy, the terminal's input and output, and the first message naming `startup.md`.
For Claude Code that is `claude --append-system-prompt-file role.md --add-dir <copy> [--model <model>] "<first message>"`; `[director] effort` is not passed, since Claude Code reads effort from its own settings.
The terminal's interrupt reaches the runtime, which handles it; the supervisor waits for the runtime to end, records the session `stopped`, and exits 0, or 1 naming the status when the runtime exited nonzero.
On Unix a hangup or termination of the supervisor, such as the terminal closing, is passed on to the runtime as a termination; the supervisor waits for it to end, records the session `stopped`, and exits 1 naming the signal.
A signal the supervisor handles that arrives after the session is recorded but before the runtime starts starts no runtime: the session is recorded `stopped` and the supervisor exits 1 naming the signal.
A session already stopped or superseded by a newer one is left as it is.
While the runtime runs, the supervisor reads its session about once a second; once [`director stop`](#stop) has recorded it `stopping`, it ends the runtime, records the session `stopped` and exits 0.
A reload request that starts a new session in the same supervisor lands with handoff notes.

## Skills

The release ships the Director's own skills beside the stage skills, each rendered into every session ([decision](decisions/director-skills.md)):

- `guildhall-director-startup`: handle `startup.md`, work each in-flight item, then `director ready`.
- `guildhall-director-inbox`: on every wake line and turn start, read `director status` and each unread message, act and acknowledge.
- `guildhall-director-tasks`: turn what the Guildmaster says into commands, from giving work and answering decisions to merging, retrying, stopping, rulings and pausing.
- `guildhall-director-stage`: run a [Director stage](interpreter.md#director-stages) from its brief and finish it with `step finish`.
- `guildhall-handoff`: record a handoff note and reload; the Guildmaster can type `/guildhall-handoff`.

Between them they name every command of the [README's list](../README.md#the-commands) the Guildmaster would otherwise type ([D57](decisions/0057-director-conversation-first.md)).
The handoff and reload commands they name are designed and land later.

## Ready

```sh
guildhall director ready [--copy <dir> | --store <file>]
```

`director ready` moves the newest session from `recovering` to `running`, which records its ready time, and prints the session as one JSON line.
A `running` session is printed unchanged, exit 0; no session, or one that is `paused`, `stopping` or `stopped`, is exit 1.

While the newest session is `recovering` it starts no new work: `task new`, and `task start` of a task with no attempt yet, are refused with exit 1 naming `guildhall director ready`, and record nothing.
Everything else is unaffected, so the Director recovers what is in flight first: `task start` of a task already under way, `step done`, `step retry`, `decide answer`, `messages` and every read.

## Pause and resume

```sh
guildhall director pause [--copy <dir> | --store <file>]
guildhall director resume [--copy <dir> | --store <file> --state <dir>]
```

`director pause` moves the newest session from `running` to `paused` and prints it as one JSON line ([decision](decisions/director-pause-stop.md)).
A paused session starts no new attempt, while attempts already running finish:

- `task start` and `step retry` print `{"wait":"paused"}` and exit 0, recording nothing.
- `task start --decision` still feeds the Guildmaster's answer, but starts no next attempt: it prints `{"wait":"paused","step":"<step>"}`, which its `next.json` records, and [`needs_me`](#status) lists the task as `paused`.
- `step done` still takes the attempt's result and opens any decision it leads to, but starts no next attempt: it prints `{"wait":"paused","step":"<step>"}`, which its `next.json` records, and [`needs_me`](#status) lists the task as `paused`.
- `decide answer`, `messages`, `artisan stop` and every read are unaffected.

`director resume` moves a paused session back to `running` and prints one JSON line, `{"session":{...},"needs_me":[...]}`, with the [status feed](#status)'s `needs_me`, which the Director then works, such as the `task start` of each held task.
It is the Director's resume, not [`artisan resume`](artisan.md#commands), which settles one attempt whose launcher is gone.

A paused session is printed unchanged by `pause`, and a running one by `resume`, exit 0.
Both are exit 1 for no session, a session that is `recovering` (run `director ready`), `stopping` or `stopped`, and a session whose supervisor is gone, naming `director stop`.

## Stop

```sh
guildhall director stop [--now] [--copy <dir> | --store <file> --state <dir>]
```

`director stop` ends the Director session and its Artisan attempts ([decision](decisions/director-pause-stop.md)).
It records the newest session `stopping`, then stops every running attempt in the store as [`artisan stop`](artisan.md#commands) does, and prints the session as one JSON line.
A stopping session starts no new attempt, as a paused one does.
The session's supervisor then ends the runtime and records the session `stopped`: after a grace period of 60 seconds in which the runtime may end by itself, or, with `--now`, at once.
A stopping session is stopped again without changing it, and `stop --now` after a plain stop ends the runtime at once.
Each attempt that cannot be proven stopped is reported on standard error and makes the exit 1; the session stays `stopping`.

A session whose supervisor's identity is gone cannot end its runtime, so `stop` records it `stopped` with the end reason `superseded`, and still stops its attempts.
No session, or a session already `stopped`, is exit 1.

The stop does not yet ask the Director to write a handoff note first or wait for the end of its turn; both land with handoff notes.

## Guard rails

The guard rails are limits the binary enforces on a Director whatever it asks ([decision](decisions/guard-rails.md)); `config.GuardRails` reads all three from the config.
In a standalone copy they are the copy's own config, which the Guildmaster edits; managed mode will make them read-only to the Director ([modes](director-modes.md#guard-rails)).

- Allowed repositories: the config's `[[repository]]` entries.
  `task new`, `task start`, `step done`, `step finish`, `step retry` and `messages send` refuse any other repository with exit 1, `guard rail repositories: the config does not list repository "<id>"`, before writing anything.
  `messages send` checks the envelope's repository when it has a config: the copy's, or one given with `--config`.
- Merge rule: each repository's `merge_policy`, which [deliver](interpreter.md#publish-and-deliver) enforces.
- Attempt caps: [`max_running_attempts`](configuration.md#settings), default 4, and `max_attempts_per_day`, default 40, across all the Director's repositories.
  An attempt runs from its start to its finish, and a day is a UTC day.
  The caps count attempts, not tokens or cost, since no runtime reports cost to Guildhall yet.
  Logging or estimating each attempt's token use and cost is a follow-up and is not built.

An advance whose next attempt would pass a cap, from `task start`, `step done`, `step finish`, `step retry` or a launcher's own advance, records and launches nothing and exits 1 naming the rail, such as `guard rail max_running_attempts: ... 4 attempts are running, the cap is 4`.
It also sends the Director a `budget-refused` message, once per task and UTC day.
The count and the start are one store write, so two advances never both take the last place, and an attempt already recorded is always launched again after a crash.
Nothing starts the refused attempt by itself: once an attempt finishes or the day turns, the Director runs `guildhall task start` for the task again, or `step retry` again for a refused retry.

## Status

```sh
guildhall director status --json [--copy <dir> | --store <file> --state <dir> [--config <file>]]
```

`director status` is the Director's status feed and the Guildmaster's "what needs me" query in one read-only JSON document ([decision](decisions/director-status-feed.md)).
firstmate's external monitor reads it, the Director's skills run it at every turn, and the session supervisor renders its `needs_me` into a new session's [startup instructions](#start).

It reads the store in one read transaction, opened read-only and waiting up to 30 seconds for a writer's lock as every advance does, so it never writes, never creates a store and succeeds once a writer lets go.
It then reads files under the state directory: each task's newest `next.json` and the attempt directories of running attempts, which it classifies with [`reconcile.Classify`](artisan.md#reconcile) and the launcher identity probe; classifying may create an attempt's `attempt.lock`, as `artisan status` does.
It prints one JSON line and exits 0; a missing store is exit 1 naming `guildhall store init`, a store that cannot be read is exit 1, and a usage error, including a missing `--json`, is exit 2.
With a [copy](running.md#copies), `--copy <dir>` or `GUILDHALL_COPY` stands in for `--store`, `--state` and `--config`.

The document has these sections:

- `as_of`: labelled like the [task inspection reads](tasks.md#inspection).
  `partial` is always `true`; `excluded` names what the feed does not inspect: `worker files`, `transcripts` and `forge state`.
  `sequence` and `committed_at` are the newest mutation committed anywhere in the store when the read began; an empty store has sequence 0.
  Live attempt state is read after the store, so it can be newer than `sequence`.
- `session`: the newest Director session: its `state`, `number`, supervisor `identity`, `release`, `pane` when bound, `started_at`, `ready_at` and `ended_at` once set, `stop_now` for a `stop --now`, and the `end_reason` of a session [stopped](#stop) as `superseded`.
  A store with no session reports only its `state`, `"no session"`, and `guard_rails`.
  `guard_rails` are the [guard rails](#guard-rails): the config's `repositories`, `merge_policies`, `max_running_attempts` and `max_attempts_per_day`, when the copy has a config, and always `running_attempts`, `attempts_today` and the UTC `day` they count, read in the same store transaction.
  A config that exists but cannot be read gives `config_error` in place of the rails.
  The identity's liveness is not probed yet.
- `mode`: `{"name":"standalone"}` until managed mode lands.
- `tasks`: every task in repository then id order, with its `state` (`open`, `done` or `cancelled`), bound `workflow`, and:
  - `next`: the `next.json` the task's last advance wrote beside its task lock, at `<state>/<repo>/<task>/next.json`, copied as JSON exactly as written; absent when there is none.
    A file that is not JSON is reported as `next_error` instead.
    Every `task start`, `step done`, `step finish` and `step retry`, typed or run by the [watcher](running.md#the-watcher), writes it, so it is absent only for a task never advanced.
  - `steps`: for each step with an attempt, in the order the steps first started, its latest `attempt` with its `outcome`, or, while it runs, `live`: the classification `state` and `reason` from `reconcile.Classify`.
    A running attempt with neither an ownership record nor an attempt directory is `absent`, as `artisan status` reports it.
- `decisions`: every open decision in repository, task and id order, with its `kind`, `subject`, `options`, `question` and `opened_at`, the commit time of its opening.
- `messages`: the Director's `unread` count, the count `by_type`, and the `headers` of at most the 20 oldest unread messages (`id`, `type`, `repository`, `task_id`, `step` and `attempt` when given, `sequence`).
- `recent`: the last 20 receipts of any task across repositories, in commit order, each with its operation, mutation id, sequence, commit time and record name, so records such as delivery's `publish-<k>-pr` documents and its `merged`, `merge-refused` and `workspace-kept` messages appear without a change to the feed.
- `needs_me`: what is waiting on the Director or the Guildmaster, each item with its `kind`, the task and attempt or decision it concerns, a `reason`, and the `command` the Director would run next, written without `--copy` because a Director session sets `GUILDHALL_COPY`.
  Items come in this order, and within a kind by repository, task, then step or message order:
  1. `advance`: an open task's step whose latest attempt is finished while the task's `next.json` still says `"wait":"attempt"` for that attempt, as a [watcher](running.md#the-watcher) whose advance failed leaves it; `guildhall step done --repo <r> --task <t> --step <s> --attempt <n> --result <file>` for a `succeeded` attempt whose result file exists, otherwise `guildhall task start --repo <r> <t>`.
  2. `retry`, `retryable: true`: an open task's step whose latest attempt finished `failed` or `stopped`; `guildhall step retry --repo <r> --task <t> --step <s> --attempt <n>`, to which the Director adds `--reason`.
  3. `resume`, `resume: true`: a running attempt of an open task classified `stuck`, `exited` or `unknown`; `guildhall artisan resume --repo <r> --task <t> --step <s> --attempt <n>`.
  4. `stalled`: an unread `worker-stalled` message; `guildhall messages read <id>`.
  5. `decision`: an open decision; `guildhall decide show --repo <r> --task <t> --decision <id>`.
  6. `messages`: one item counting the other unread messages; `guildhall messages list director`.
  7. `director`: an open task whose `next.json` says `"wait":"director"`; `guildhall task timeline --repo <r> --json <t>`.
  8. `paused`: an open task whose `next.json` says `"wait":"paused"`, held while the Director was [paused](#pause-and-resume); `guildhall task start --repo <r> <t>`.

  The newest Director session, while it is `recovering`, comes before all of them as a `recovering` item; `guildhall director ready`.
  Done and cancelled tasks contribute no `advance`, `retry`, `resume`, `director` or `paused` item.

Times are commit times of the store's receipts, in UTC.
The feed reads every receipt in the store, so its cost grows with the store's mutation count, as `task timeline`'s does.
