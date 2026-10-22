# Store

Each Director copy keeps its durable records in one store file owned by Guildhall (R1.17).
`internal/store` implements it as a single bbolt database.
The store supports the [local messaging commands](messaging.md), the [decision commands](decisions-cli.md), and the [owned Artisan commands](artisan.md#commands) and [`task start`, `step done` and `step retry`](running.md), which launch and reconcile attempts from its ownership records; nothing schedules or merges work from it yet.

## Records

Every record is keyed by repository id, so the same task id in two repositories names two tasks.
Ids use the workflow id form: lowercase letters, digits and hyphens, starting with a letter, except for [message ids](messaging.md#envelope).

| Record | Key | Contract |
|---|---|---|
| Task | repository, task id | Title, the workflow revision (name and `sha256:` digest), the `sha256:` digest of the configuration it was accepted under, and the tasks it depends on. The revisions never change after creation (R1.62). State starts `open` and moves once to `done` or `cancelled`. Dependencies must already exist, in any repository of the same Director. |
| Stage attempt | repository, task, step, number | Numbered from 1 per task and step. Running until finished as `succeeded`, `failed` or `stopped`, once; the [first finish stands](#attempts). A step cannot start a new attempt while one is running, and a task that is not open cannot start one. |
| Document reference | repository, task, name | A slash-separated relative path and the lowercase hex sha256 of the file. The document itself is a file, not a store record (R1.13). A reference never changes once recorded. A review outcome's reference also projects its findings and dispositions (`review`), so inspection never opens the file; finding ids must be valid ids with non-blank summaries and disposition states `resolved`, `still-open` or `withdrawn`. |
| Pane binding | repository, task | Herdr session, workspace, tab and pane. Presentation only: binding, rebinding and unbinding never change task state (R1.52). |
| Workflow approval | workflow digest | The Guildmaster's approval of one exact workflow revision for this Director: the workflow name, its `sha256:` digest, the approval words exactly as given and the UTC time it was recorded. Recorded once per digest; see [workflow-edit authority](configuration.md#workflow-edit-authority). Not per repository and never changes a task. |
| Decision | repository, task, decision id | A question for the Guildmaster and, once, the exact answer (R1.61). A decision is typed: its kind is `approval`, `review`, `loop-exit`, `merge` or `question`; every kind but `question` names the subject the answer binds to, a document (`sha256:<64 lowercase hex>` digest) or a head (40 or 64 lowercase hex object id); and it may offer up to 16 one-line options. When it offers options, the answer's first line (the text before the first LF, with only a single trailing CR removed) must equal one exactly or the answer is refused and nothing is written; the answer is still recorded verbatim, with the matched option as `choice` and the trimmed text after the first line as `note`. Decisions recorded before typed decisions have no kind, subject or options and take any answer. Answering records the answer only and never advances the task. |
| Ruling | repository, task, outcome document, finding id | The Director's `out-of-scope`, `duplicate` or `defer` ruling on one finding of a recorded review outcome, with a non-blank reason; recorded once, `defer` only for a finding that cites no rule. Contested at most once, recording the attempt that contested it. See [Director rulings](tasks.md#director-rulings). |
| Full-review request | repository, task, number | The Director's request that the task's next code review round be full, numbered from 1 per task; at most one is pending. Consumed once, recording the attempt it made full. See [full review](tasks.md#full-review). |
| Attempt ownership | repository, task, step, attempt number | Which launcher owns a running stage attempt: its process identity (PID and creation token), its [containment recovery description](#containment-record), its control endpoint and a state of `starting`, `running` or `released`; see [attempt ownership](#attempt-ownership). |
| Director session | session number | One run of the Director's runtime under its supervisor: the supervisor's process identity, the Guildhall release, the `sha256:` digest of the configuration it started under, the Herdr pane Guildhall created for it if any, a state, whether a stop asked it to end at once, and an end reason when one was recorded; see [Director sessions](#director-sessions). Not per repository. |
| Wake | sequence | One wake decision for one message to the Director: the message id, the sender type, `woke` or `queued` with a one-line reason of at most 256 bytes, and the UTC time; at most one per message, written by [the Director's wake](messaging.md#waking-the-director). Not per repository. |
| Message | repository, message id | A message to the Director or to the Artisan of a running stage attempt, about an existing task and optionally one of its attempts, with an inbox entry until it is acknowledged once. Message ids are unique in the store. Messages never change other records; see [messaging](messaging.md). |

Options must be non-blank valid UTF-8 without line breaks or surrounding white space, distinct and at most 256 bytes each.
Titles, questions, answers and approval words must be non-blank valid UTF-8 and are limited to 64 KiB per field; larger content belongs in a referenced document.
Message payload limits and artifact requirements are defined in the [envelope contract](messaging.md#envelope).
Pane binding fields must be non-blank valid UTF-8 and are limited to 256 bytes each.
An ownership record's launcher token is limited to 256 bytes, and its endpoint to 1024 bytes.
Containment fields follow the [containment record](#containment-record) rules.
Document paths are relative to the Director's document directory and must be clean, local, slash-separated paths of at most 1024 bytes, without backslashes or colons.
Document paths must be valid UTF-8; invalid UTF-8 is refused with `ErrInvalid` before hashing or persistence.

## Mutations and receipts

Every change is a mutation that names a caller-chosen mutation id of 1 to 128 characters from letters, digits, `.`, `_`, `:` and `-`, starting with a letter or digit.
A mutation runs in one bbolt transaction that writes its records and a receipt together and commits durably before the call returns.
After a crash, either the records and the receipt are both present or neither is.

A receipt holds the mutation id, the operation, a sha256 digest of the operation and request, the repository and task, the attempt number where one applies, the record it wrote, a store-wide sequence number starting at 1 and the commit time in UTC.
The record is named within its task: `task` for creating a task and changing its state, `attempt/<step>/<number>` for starting and finishing an attempt, `document/<name>`, `pane` for binding and unbinding, `decision/<id>` for opening and answering, `owner/<step>/<number>` for taking and updating an attempt's ownership, `ruling/<document>/<finding>` for recording and contesting a ruling, `full-review/<n>` for requesting and consuming a full review, `message/<id>` for sending and acknowledging a message, and `wake/<id>` for recording the wake for message `<id>`.
Director session mutations are outside any task and name the record `director/session/<n>`.
It is not part of the request digest, so receipts written before it existed have no record and still replay; mutations outside a task, such as workflow approvals, have none.
`TaskHistory` reads a task, its attempts, documents, decisions, rulings and full-review requests, and every receipt for it in one read transaction, labelled with the newest committed sequence; ownership records are read separately with `Ownership`.
It scans every receipt, so its cost grows with the store (see [inspection](tasks.md#inspection)).
`DirectorView` reads, in one read transaction, every task and attempt, the unreleased ownership records, the open decisions, the Director's unacknowledged messages, the newest Director session and every receipt that names a task, labelled the same way; it is what [`director status`](director.md#status) reads, and it scans every receipt too.
The launcher's [heartbeat](artisan.md#heartbeat) is a file in the attempt directory, not a store record, so it writes no receipt.
Sending message `<id>` records the mutation `message:<id>`, and acknowledging it records `message-ack:<id>`; a message's sequence is its send receipt's sequence.
Starting Director session `<n>` records the mutation `director-session:<n>`, moving it to a state records `director-state:<n>:<state>:<from>`, where `<from>` is the receipt sequence of the state it leaves, and recording the wake for message `<id>` records `wake:<id>`.
Approving workflow digest `<digest>` records the mutation `workflow-approval:<digest>`, so the same approval replays and different words for an approved digest conflict.
The [decision command reference](decisions-cli.md#answers) defines answer mutation ids (D23).

- Replaying a mutation id with the same operation and request returns the original receipt and changes nothing, even after a restart and even when later mutations have changed the records since.
- Reusing a mutation id for a different operation or request fails with `ErrMutationConflict` and changes nothing.
- A mutation that fails writes nothing, consumes no sequence number and leaves its id free for a later request.

A caller that loses the result of a mutation, for example because the process stopped, retries with the same mutation id and gets the original receipt.

## Attempts

An attempt has one outcome, and the first finish stands.
Several writers finish attempts, each under its own mutation id: the launcher when its worker ends (`launch-finish:<tag>`), the interpreter's result intake (`attempt-finish:…`) and `artisan resume` settling an exited launcher's attempt (`resume-finish:<tag>`); any of them may write first, so the rule is the store's and no writer repeats it.

- `FinishAttempt` on an attempt already finished with the same outcome, under a different mutation id, returns the receipt of the mutation that finished it and writes nothing: the new id gets no receipt and no sequence number, so it stays free.
- A different outcome is refused with `ErrInvalid` (`already finished`) and writes nothing.
- Finding the finishing receipt scans every receipt by operation and request digest, including legacy receipts without a record label; it happens only when a second writer arrives.

`StartCappedAttempt` is `StartAttempt` under the Director's [attempt caps](director.md#guard-rails): in the same mutation it counts the attempts started and not finished in every repository, and the `start-attempt` and `start-owned-attempt` receipts committed since the start of the current UTC day by the store's clock, and refuses with a `*CapRefusal`, wrapping `ErrAttemptCap`, at either cap, writing nothing.
The caps are not part of the request, so a replay returns its receipt whatever they are now.
`AttemptUsage`, and `DirectorView`'s `Usage`, read the same counts without writing; like the view they scan every receipt.

## One writer

A store has exactly one writer at a time.
This uses bbolt's own kernel file lock and no second lock: a writable open takes an exclusive lock (`flock` on Linux and macOS, `LockFileEx` on Windows) and a read-only open takes a shared lock.
Opening waits at most the lock timeout, two seconds by default, and then fails with `ErrLocked`.
The wait is a poll of the kernel lock with no queue, so contending openers are not served in order: past the number the timeout can serve, an opener is refused with `ErrLocked` even though others got in.
So a second writer is refused, and a read-only open is refused while a writer holds the store.
The lock belongs to the open file, so two opens in one process exclude each other as two processes do.
The kernel releases the lock when the holding process exits, including when it is killed, and committed mutations survive.

Within one open store, calls are safe from many goroutines; bbolt serializes writes.
`Close` waits for in-flight calls to finish, then releases the lock; later calls fail with `ErrClosed`, and closing again does nothing.

## Director sessions

The newest Director session is the Director's current one; the session supervisor, [`director start`](director.md#start), records it and its state moves, and [`director status`](director.md#status) reports it.
`StartDirectorSession` records session `<n>` as `recovering` when `<n>` is one more than the newest session's number, or 1 in a store with none; the store sets its start time and refuses a session given any time or state sequence.
Starting it records every earlier session that has not stopped as `stopped`, with the end reason `superseded`, in the same transaction, so exactly one session is current.
The end reason is set only by the store: a session its supervisor recorded `stopped` has none, and a session given one is refused.

`SetDirectorState` moves only the newest session, and only forward: `recovering`, then `running` or `paused`, then `stopping`, then `stopped`, except that `running` and `paused` move to each other.
`paused` starts no new work while running attempts finish, and `stopping` lasts until the runtime ends; `stopped` never changes.
The first move to `running` sets the ready time and the move to `stopped` the end time.
A move names the state sequence it read, the receipt sequence that recorded the current state, and is refused with `ErrInvalid` when the session has moved since, so racing moves cannot overwrite each other; moving backward, staying put and moving a superseded session are refused the same way.

[`director stop`](director.md#stop) records its two moves with their own methods, under the same rules.
`RequestDirectorStop` moves a `recovering`, `running` or `paused` session to `stopping`, setting `StopNow` for `stop --now`; a `stopping` session without `StopNow` takes it, staying `stopping`, and any other repeat is refused as staying put.
Its mutation id is `director-stop:<n>:<turn|now>:<from>`.
`EndDirectorSession` moves a session that has not stopped to `stopped` with a non-blank one-line end reason of at most 256 bytes, such as `superseded` for a session whose supervisor is gone, with the mutation id `director-end:<n>:<from>`.

## Attempt ownership

An ownership record says which per-attempt launcher owns a running stage attempt, so a second launcher cannot run the same attempt while the first is alive.
The check and the write happen in one mutation under the store's single-writer lock, so no separate lock file is needed.
The ownership record holds the current launcher identity; the receipt holds the request digest and commit time, not a historical copy of that identity.

- `StartOwnedAttempt` starts the next attempt of a step and records its owner in the same mutation, so a crash leaves both or neither.
- `TakeOwnership` records an owner for an attempt that is already running.
  An attempt with no owner is taken at once.
  An attempt with an owner is probed first: a live owner refuses the take with `ErrOwnerLive`, and an owner that cannot be proven exited refuses it with `ErrOwnerUnknown`; a refusal writes nothing.
  Only an owner proven exited, with no probe error, may be replaced.
  The caller supplies the probe; the store never probes processes, and a nil probe is refused with `ErrInvalid` and writes nothing, even for an attempt with no owner.
  Owner identities are `ProcessIdentity` values (PID and creation token), and a probe answers with a `Liveness` value: `gone`, `alive` or `unverified`, the words Artisan's native probe reports; any other value, including the empty one, is unknown.
  `ProbeLauncher` over the native identity probe is the conservative policy: it refuses with `ErrOwnerUnknown` whenever containment is recorded, without consulting the identity probe, because launcher exit alone cannot establish absence of contained survivors.
  Without containment it checks the recorded launcher identity, so a reused PID with a different creation token counts as exited.
  Recovery of recorded containment requires an explicit containment-aware probe proving both launcher exit and absence of survivors; it reports surviving members as live or unknown, never as exited.
  `artisan start` passes `launch.ProbeOwner`, that containment-aware probe: alive while the launcher lives, gone once it is gone and `artisan.Contained` proves its unit empty.
- Both record the owner as `starting`, with the containment the request carries: `artisan start` records a job or cgroup unit's description, without a worker, from the moment it takes ownership.
  `UpdateOwnership` then lets the same launcher identity record its containment unit, its endpoint and `running`; another launcher's update is refused with `ErrOwnerLive`, and `running` never returns to `starting`.
  Recovery evidence is monotonic: once the record holds a containment or an endpoint, every later update must carry an equal value, except that a containment recorded without its worker may have the worker identity added once; an update that otherwise clears or changes either is refused with `ErrInvalid` and writes nothing, so a stale or partial progress write cannot turn a contained attempt into one `ProbeLauncher` lets be taken over.
- `FinishAttempt` releases the attempt's ownership record in the same mutation and keeps it, still naming its launcher; a released record is never updated or taken again, because a finished attempt cannot be owned.
- A replayed mutation id returns its original receipt without probing, as every mutation does.

### Containment record

The ownership record's `containment` is the recovery description of the unit the owner's worker runs in, so a later sweep can find that unit after the launcher is gone.
Every [Artisan containment kind](artisan.md#recording-the-containment) has one, copied field for field from the unit's `Containment` plus the worker identity `Start` returned:

```json
{"kind": "subreaper+pgroup", "reason": "no cgroup v2 mounted at /sys/fs/cgroup", "worker": {"pid": 4242, "token": "boot:…:91234"}}
```

| Kind | `name` | `worker` |
|---|---|---|
| `job` | The job name, required | Optional |
| `cgroup` | The cgroup directory, required | Optional |
| `subreaper+pgroup` | Must be empty | Required: the group is found by its leader, the worker |
| `pgroup` | Must be empty | Required: the group is found by its leader, the worker |

A process group has no name of its own, and no group id or other invented name may stand in for the worker identity, because a group id is reused once its leader is gone.
`reason` keeps the Linux detection result that chose the kind, such as why no delegated cgroup v2 was usable; it may be omitted.
When present, `name`, `reason` and the worker token must be non-blank valid UTF-8 within `MaxContainmentBytes` (1024 bytes each); a recorded worker must also have a positive PID.
`Containment.Recoverable` checks structural recovery evidence, not identity liveness or whether the unit is empty.
It fails with `ErrUnrecoverable` for an unknown kind or a violation of these field requirements.
Writes refuse a containment that is not recoverable with `ErrInvalid`.
Records written before this rule, whose containment held only `kind` and `name`, still read unchanged; a recoverable one keeps working, and one that is not never authorises a sweep or a take-over: `TakeOwnership` refuses it with `ErrOwnerUnknown` without probing, even when its launcher has exited.

Taking ownership records the mutation id the caller chooses.
The [launcher composition](../internal/launch/launch.go), `internal/launch`, owns its mutation-id convention: `launch-<operation>:<tag>`, where the tag is a hex digest of the attempt key and the launcher's identity.
Reusing an id with a different creation token returns `ErrMutationConflict`, never a new take-over.

## File format and versions

`store.Open` normally creates and initializes a missing or empty store file.
`store.Options.RequireStore` requires an already initialized Guildhall store, even for a writable open: missing, empty and non-store files are refused with `ErrNotStore` without creating or modifying them.
It checks existing metadata instead of initializing buckets, while leaving successful opens writable unless `ReadOnly` is also set.
All messaging commands use this option; `send` and `ack` cannot initialize a store as a side effect of a refused operation.

The file holds a `meta` bucket (`format = guildhall-store`, `schema_version`), a `receipts` bucket keyed by mutation id, an optional `workflow-approvals` bucket keyed by workflow digest, an optional `director` bucket holding `sessions` keyed by session number and `wakes` keyed by sequence, and a `repositories` bucket with one nested bucket per repository holding `tasks`, `attempts`, `owners`, `documents`, `panes`, `decisions`, `messages` and `inbox`.
`inbox` indexes unacknowledged messages by recipient and sequence, holding each message's id.
A per-repository bucket is created by the first record that needs it, so a schema 1 file written before messages existed accepts them unchanged.
The `workflow-approvals` bucket is likewise created by the first approval, and the `director` bucket by the first session or wake; neither is required, and a store without one has no such records.
Records and receipts are JSON.

Opening refuses a file that is not a Guildhall store (`ErrNotStore`), including another program's bbolt file, and a store whose `schema_version` is newer than this Guildhall supports (`ErrNewerSchema`); a refusal releases the lock.
Both writable and read-only opens refuse an existing store missing any required top-level bucket (`meta`, `receipts` or `repositories`) with `ErrNotStore`.
Required buckets are created only when initializing an empty file, never to repair an existing store.
A read-only open never creates, initializes or writes a file.

The repository holds a schema 1 store file (`internal/store/testdata/schema-v1.store`) that the tests open on Linux, Windows and macOS in CI, so every platform reads a file another platform wrote.
bbolt records the page size in the file, and every release target is little-endian.

## `guildhall store init`

`guildhall store init <path>` creates the file at `<path>` and initializes it as an empty schema 1 store with `store.Open`, then prints `{"store": "<path>", "schema_version": 1}` and exits 0.
It refuses anything already at `<path>`, whether a store, an empty file, another file or a directory, with `store.ErrExists` naming the path, and exits 1 without changing it: it never overwrites or repairs.
The file is created exclusively, so something appearing after the check is still refused; a failed initialization removes the file it created, and a missing parent directory is not created.
Usage errors exit 2.
`store init` is the only command that creates a store; every other command opens with `RequireStore` and, given a missing store, names `store init`.

## `guildhall store doctor`

`guildhall store doctor <path>` opens a store read-only, runs bbolt's page-level integrity check, decodes every record and prints the schema version, the receipt count, the workflow approval count and each repository's record counts, including rulings, ownership records and how many of them are in `starting` with no containment (owners that cannot be proven exited once their launcher is gone; [limits](artisan.md#limits)), messages and how many are unread.
It exits 0 when the store opened and showed no problems, and 1 when it is missing, locked by a writer, not a store, newer than this Guildhall or damaged.

Both commands take `--copy <dir>`, or `GUILDHALL_COPY`, in place of `<path>`, naming the copy's `director.store`; a path given with `--copy` must be that file, and a path given without it overrides `GUILDHALL_COPY` ([copies](running.md#copies)).
