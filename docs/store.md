# Store

Each Director copy keeps its durable records in one store file owned by Guildhall (R1.17).
`internal/store` implements it as a single bbolt database.
The store supports the [local messaging commands](messaging.md) and the [decision commands](decisions-cli.md), but nothing in Guildhall launches, schedules or merges work from it yet.

## Records

Every record is keyed by repository id, so the same task id in two repositories names two tasks.
Ids use the workflow id form: lowercase letters, digits and hyphens, starting with a letter, except for [message ids](messaging.md#envelope).

| Record | Key | Contract |
|---|---|---|
| Task | repository, task id | Title, the workflow revision (name and `sha256:` digest), the `sha256:` digest of the configuration it was accepted under, and the tasks it depends on. The revisions never change after creation (R1.62). State starts `open` and moves once to `done` or `cancelled`. Dependencies must already exist, in any repository of the same Director. |
| Stage attempt | repository, task, step, number | Numbered from 1 per task and step. Running until finished as `succeeded`, `failed` or `stopped`, once. A step cannot start a new attempt while one is running, and a task that is not open cannot start one. |
| Document reference | repository, task, name | A slash-separated relative path and the lowercase hex sha256 of the file. The document itself is a file, not a store record (R1.13). A reference never changes once recorded. A review outcome's reference also projects its findings and dispositions (`review`), so inspection never opens the file; finding ids must be valid ids with non-blank summaries and disposition states `resolved`, `still-open` or `withdrawn`. |
| Pane binding | repository, task | Herdr session, workspace, tab and pane. Presentation only: binding, rebinding and unbinding never change task state (R1.52). |
| Workflow approval | workflow digest | The Guildmaster's approval of one exact workflow revision for this Director: the workflow name, its `sha256:` digest, the approval words exactly as given and the UTC time it was recorded. Recorded once per digest; see [workflow-edit authority](configuration.md#workflow-edit-authority). Not per repository and never changes a task. |
| Decision | repository, task, decision id | A question for the Guildmaster and, once, the exact answer (R1.61). A decision is typed: its kind is `approval`, `review`, `loop-exit`, `merge` or `question`; every kind but `question` names the subject the answer binds to, a document (`sha256:<64 lowercase hex>` digest) or a head (40 or 64 lowercase hex object id); and it may offer up to 16 one-line options. When it offers options, the answer's first line (the text before the first LF, with only a single trailing CR removed) must equal one exactly or the answer is refused and nothing is written; the answer is still recorded verbatim, with the matched option as `choice` and the trimmed text after the first line as `note`. Decisions recorded before typed decisions have no kind, subject or options and take any answer. Answering records the answer only and never advances the task. |
| Ruling | repository, task, outcome document, finding id | The Director's `out-of-scope`, `duplicate` or `defer` ruling on one finding of a recorded review outcome, with a non-blank reason; recorded once, `defer` only for a finding that cites no rule. Contested at most once, recording the attempt that contested it. See [Director rulings](tasks.md#director-rulings). |
| Attempt ownership | repository, task, step, attempt number | Which launcher owns a running stage attempt: its process identity (PID and creation token), its [containment recovery description](#containment-record), its control endpoint and a state of `starting`, `running` or `released`; see [attempt ownership](#attempt-ownership). |
| Message | repository, message id | A message to the Director or to the Artisan of a running stage attempt, about an existing task and optionally one of its attempts, with an inbox entry until it is acknowledged once. Message ids are unique in the store. Messages never change other records; see [messaging](messaging.md). |

Options must be non-blank valid UTF-8 without line breaks or surrounding white space, distinct and at most 256 bytes each.
Titles, questions, answers and approval words must be non-blank valid UTF-8 and are limited to 64 KiB per field; larger content belongs in a referenced document.
Message payload limits and artifact requirements are defined in the [envelope contract](messaging.md#envelope).
Pane binding fields must be non-blank valid UTF-8 and are limited to 256 bytes each.
An ownership record's launcher token is limited to 256 bytes, and its endpoint to 1024 bytes.
Containment fields follow the [containment record](#containment-record) rules.
Document paths are relative to the Director's document directory and must be clean, local, slash-separated paths of at most 1024 bytes, without backslashes or colons.
Document paths must be valid UTF-8; invalid UTF-8 is refused with `ErrInvalid` before hashing or persistence.

## Attempt heartbeats

`PutHeartbeat` stores the latest fixture heartbeat per repository, task, step and attempt in the repository's `heartbeats` bucket, using the same key as an attempt.
The record contains `sequence`, `time`, `worker_state` and `last_output_at`.
Sequence must be positive and increase; times must be nonzero and last output cannot follow heartbeat time; worker state is nonblank UTF-8 bounded by `MaxTextBytes`.
Only a running, existing attempt accepts writes; missing attempts return `ErrNotFound`, while completed attempts and non-increasing sequence return `ErrInvalid`.
Mutation replay is unchanged and receipts name `heartbeat/<step>/<number>`.
`Heartbeat` returns the latest record or `ErrNotFound`; reading or writing a heartbeat never changes the attempt.
These are fixtures for the interpreter now; launcher L6 will write about every five seconds.
Heartbeats are read separately, not through `TaskHistory`.

The shared Records contract and worker-stalled integration tests were written before implementation, but no failure was observed then.
On 2026-10-21, after implementation was written, temporarily stubbing both heartbeat reads to return `ErrNotFound` made `TestRecordsContract/{store,double}/heartbeat` fail on the persisted heartbeat read (`record not found`) and `TestWorkerStalled/{double,store}` fail with `stalls = 0, want 1`.
Both implementations were restored for focused verification.
These [captured runs](evidence/heartbeat-2026-10-21.md) are post-implementation failure evidence, not historical red-first evidence.
See [store contract](contracts/store.md).

## Mutations and receipts

Every change is a mutation that names a caller-chosen mutation id of 1 to 128 characters from letters, digits, `.`, `_`, `:` and `-`, starting with a letter or digit.
A mutation runs in one bbolt transaction that writes its records and a receipt together and commits durably before the call returns.
After a crash, either the records and the receipt are both present or neither is.

A receipt holds the mutation id, the operation, a sha256 digest of the operation and request, the repository and task, the attempt number where one applies, the record it wrote, a store-wide sequence number starting at 1 and the commit time in UTC.
The record is named within its task: `task` for creating a task and changing its state, `attempt/<step>/<number>` for starting and finishing an attempt, `document/<name>`, `pane` for binding and unbinding, `decision/<id>` for opening and answering, `owner/<step>/<number>` for taking and updating an attempt's ownership, `ruling/<document>/<finding>` for recording and contesting a ruling, and `message/<id>` for sending and acknowledging a message.
Heartbeat receipts follow the [attempt heartbeat contract](#attempt-heartbeats).
It is not part of the request digest, so receipts written before it existed have no record and still replay; mutations outside a task, such as workflow approvals, have none.
`TaskHistory` reads a task, its attempts, documents, decisions and rulings, and every receipt for it in one read transaction, labelled with the newest committed sequence; ownership records are read separately with `Ownership` and heartbeats with `Heartbeat`.
It scans every receipt, so its cost grows with the store (see [inspection](tasks.md#inspection)).
Sending message `<id>` records the mutation `message:<id>`, and acknowledging it records `message-ack:<id>`; a message's sequence is its send receipt's sequence.
Approving workflow digest `<digest>` records the mutation `workflow-approval:<digest>`, so the same approval replays and different words for an approved digest conflict.
The [decision command reference](decisions-cli.md#answers) defines answer mutation ids (D23).

- Replaying a mutation id with the same operation and request returns the original receipt and changes nothing, even after a restart and even when later mutations have changed the records since.
- Reusing a mutation id for a different operation or request fails with `ErrMutationConflict` and changes nothing.
- A mutation that fails writes nothing, consumes no sequence number and leaves its id free for a later request.

A caller that loses the result of a mutation, for example because the process stopped, retries with the same mutation id and gets the original receipt.

## One writer

A store has exactly one writer at a time.
This uses bbolt's own kernel file lock and no second lock: a writable open takes an exclusive lock (`flock` on Linux and macOS, `LockFileEx` on Windows) and a read-only open takes a shared lock.
Opening waits at most the lock timeout, two seconds by default, and then fails with `ErrLocked`.
So a second writer is refused, and a read-only open is refused while a writer holds the store.
The lock belongs to the open file, so two opens in one process exclude each other as two processes do.
The kernel releases the lock when the holding process exits, including when it is killed, and committed mutations survive.

Within one open store, calls are safe from many goroutines; bbolt serializes writes.
`Close` waits for in-flight calls to finish, then releases the lock; later calls fail with `ErrClosed`, and closing again does nothing.

## Attempt ownership

An ownership record says which per-attempt launcher owns a running stage attempt, so a second launcher cannot run the same attempt while the first is alive.
The check and the write happen in one mutation under the store's single-writer lock, so no separate lock file is needed.
The ownership record holds the current launcher identity; the receipt holds the request digest and commit time, not a historical copy of that identity.

- `StartOwnedAttempt` starts the next attempt of a step and records its owner in the same mutation, so a crash leaves both or neither.
- `TakeOwnership` records an owner for an attempt that is already running.
  An attempt with no owner is taken at once.
  An attempt with an owner is probed first: a live owner refuses the take with `ErrOwnerLive`, and an owner that cannot be proven exited refuses it with `ErrOwnerUnknown`; a refusal writes nothing.
  Only an owner proven exited, with no probe error, may be replaced.
  The default probe (`ProbeLauncher`, also selected by nil) refuses with `ErrOwnerUnknown` whenever containment is recorded; launcher exit alone cannot establish absence of contained survivors.
  Without containment it checks the recorded launcher identity, so a reused PID with a different creation token counts as exited.
  Recovery of recorded containment requires an explicit containment-aware probe proving both launcher exit and absence of survivors; it reports surviving members as live or unknown, never as exited.
- Both record the owner as `starting`.
  `UpdateOwnership` then lets the same launcher identity record its containment unit, its endpoint and `running`; another launcher's update is refused with `ErrOwnerLive`, and `running` never returns to `starting`.
  Recovery evidence is monotonic: once the record holds a containment or an endpoint, every later update must carry an equal value; an update that clears or changes either is refused with `ErrInvalid` and writes nothing, so a stale or partial progress write cannot turn a contained attempt into one the default probe lets be taken over.
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
The planned launcher convention is `own:<repository>:<task>:<step>:<number>:<launcher pid>`, because mutation ids cannot contain `/`; launcher command integration is not implemented yet.
Under that convention a different PID gives a different id; reusing an id with a different creation token returns `ErrMutationConflict`, never a new take-over.

## File format and versions

`store.Open` normally creates and initializes a missing or empty store file.
`store.Options.RequireStore` requires an already initialized Guildhall store, even for a writable open: missing, empty and non-store files are refused with `ErrNotStore` without creating or modifying them.
It checks existing metadata instead of initializing buckets, while leaving successful opens writable unless `ReadOnly` is also set.
All messaging commands use this option; `send` and `ack` cannot initialize a store as a side effect of a refused operation.

The file holds a `meta` bucket (`format = guildhall-store`, `schema_version`), a `receipts` bucket keyed by mutation id, an optional `workflow-approvals` bucket keyed by workflow digest, and a `repositories` bucket with one nested bucket per repository holding `tasks`, `attempts`, `owners`, `documents`, `panes`, `decisions`, `messages` and `inbox`.
`inbox` indexes unacknowledged messages by recipient and sequence, holding each message's id.
A per-repository bucket is created by the first record that needs it, so a schema 1 file written before messages existed accepts them unchanged.
The `workflow-approvals` bucket is likewise created by the first approval; it is not required, and a store without it has no recorded approvals.
Records and receipts are JSON.

Opening refuses a file that is not a Guildhall store (`ErrNotStore`), including another program's bbolt file, and a store whose `schema_version` is newer than this Guildhall supports (`ErrNewerSchema`); a refusal releases the lock.
Both writable and read-only opens refuse an existing store missing any required top-level bucket (`meta`, `receipts` or `repositories`) with `ErrNotStore`.
Required buckets are created only when initializing an empty file, never to repair an existing store.
A read-only open never creates, initializes or writes a file.

The repository holds a schema 1 store file (`internal/store/testdata/schema-v1.store`) that the tests open on Linux, Windows and macOS in CI, so every platform reads a file another platform wrote.
bbolt records the page size in the file, and every release target is little-endian.

## `guildhall store doctor`

`guildhall store doctor <path>` opens a store read-only, runs bbolt's page-level integrity check, decodes every record and prints the schema version, the receipt count, the workflow approval count and each repository's record counts, including ownership records, messages and how many are unread.
It exits 0 when the store opened and showed no problems, and 1 when it is missing, locked by a writer, not a store, newer than this Guildhall or damaged.
