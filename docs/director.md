# The Director

The Director is the agent that runs a Director copy: it gives Artisans work, answers their messages and asks the Guildmaster for decisions ([architecture](architecture.md)).
This page describes the commands that serve the Director itself rather than one task.
Only `director status` and the [session record](store.md#director-sessions) are built; the session supervisor (`director start`) that writes the record, and the pause, stop and handoff commands, are designed in the slice 4 plan and land later.
A message to the Director [wakes it](messaging.md#waking-the-director) when its session is running in a pane Guildhall created.

## Status

```sh
guildhall director status --json [--copy <dir> | --store <file> --state <dir>]
```

`director status` is the Director's status feed and the Guildmaster's "what needs me" query in one read-only JSON document ([decision](decisions/director-status-feed.md)).
firstmate's external monitor reads it, the Director's skills run it at every turn, and the session supervisor will render its `needs_me` into a new session's startup instructions.

It reads the store in one read transaction, opened read-only and waiting up to 30 seconds for a writer's lock as every advance does, so it never writes, never creates a store and succeeds once a writer lets go.
It then reads files under the state directory: each task's newest `next.json` and the attempt directories of running attempts, which it classifies with [`reconcile.Classify`](artisan.md#reconcile) and the launcher identity probe; classifying may create an attempt's `attempt.lock`, as `artisan status` does.
It prints one JSON line and exits 0; a missing store is exit 1 naming `guildhall store init`, a store that cannot be read is exit 1, and a usage error, including a missing `--json`, is exit 2.
With a [copy](running.md#copies), `--copy <dir>` or `GUILDHALL_COPY` stands in for `--store` and `--state`.

The document has these sections:

- `as_of`: labelled like the [task inspection reads](tasks.md#inspection).
  `partial` is always `true`; `excluded` names what the feed does not inspect: `worker files`, `transcripts` and `forge state`.
  `sequence` and `committed_at` are the newest mutation committed anywhere in the store when the read began; an empty store has sequence 0.
  Live attempt state is read after the store, so it can be newer than `sequence`.
- `session`: the newest Director session: its `state`, `number`, supervisor `identity`, `release`, `pane` when bound, and `started_at`, `ready_at` and `ended_at` once set.
  A store with no session reports only `{"state":"no session"}`.
  The identity's liveness is not probed yet.
- `mode`: `{"name":"standalone"}` until managed mode lands.
- `tasks`: every task in repository then id order, with its `state` (`open`, `done` or `cancelled`), bound `workflow`, and:
  - `next`: the `next.json` the task's last advance wrote beside its task lock, at `<state>/<repo>/<task>/next.json`, copied as JSON exactly as written; absent when there is none.
    A file that is not JSON is reported as `next_error` instead.
    Every `task start`, `step done` and `step retry`, typed or run by the [watcher](running.md#the-watcher), writes it, so it is absent only for a task never advanced.
  - `steps`: for each step with an attempt, in the order the steps first started, its latest `attempt` with its `outcome`, or, while it runs, `live`: the classification `state` and `reason` from `reconcile.Classify`.
    A running attempt with neither an ownership record nor an attempt directory is `absent`, as `artisan status` reports it.
- `decisions`: every open decision in repository, task and id order, with its `kind`, `subject`, `options`, `question` and `opened_at`, the commit time of its opening.
- `messages`: the Director's `unread` count, the count `by_type`, and the `headers` of at most the 20 oldest unread messages (`id`, `type`, `repository`, `task_id`, `step` and `attempt` when given, `sequence`).
- `recent`: the last 20 receipts of any task across repositories, in commit order, each with its operation, mutation id, sequence, commit time and record name, so records such as delivery's `publish-<k>-pr` documents and its `merged`, `merge-refused` and `workspace-kept` messages appear without a change to the feed.
- `needs_me`: what is waiting on the Director or the Guildmaster, each item with its `kind`, the task and attempt or decision it concerns, a `reason`, and the `command` the Director would run next, written without `--copy` because a Director session sets `GUILDHALL_COPY`.
  Items come in this order, and within a kind by repository, task, then step or message order:
  1. `retry`, `retryable: true`: an open task's step whose latest attempt finished `failed` or `stopped`; `guildhall step retry --repo <r> --task <t> --step <s> --attempt <n>`, to which the Director adds `--reason`.
  2. `resume`, `resume: true`: a running attempt of an open task classified `stuck`, `exited` or `unknown`; `guildhall artisan resume --repo <r> --task <t> --step <s> --attempt <n>`.
  3. `stalled`: an unread `worker-stalled` message; `guildhall messages read <id>`.
  4. `decision`: an open decision; `guildhall decide show --repo <r> --task <t> --decision <id>`.
  5. `messages`: one item counting the other unread messages; `guildhall messages list director`.
  6. `director`: an open task whose `next.json` says `"wait":"director"`; `guildhall task timeline --repo <r> --json <t>`.

  A Director session in `recovering` will be the first item once the session supervisor lands.
  Done and cancelled tasks contribute no `retry`, `resume` or `director` item.

Times are commit times of the store's receipts, in UTC.
The feed reads every receipt in the store, so its cost grows with the store's mutation count, as `task timeline`'s does.
