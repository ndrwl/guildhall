# Store

Each Director copy keeps its durable records in one store file owned by Guildhall (R1.17).
`internal/store` implements it as a single bbolt database.
The store supports the [local messaging commands](messaging.md), but nothing in Guildhall launches, schedules or merges work from it yet.

## Records

Every record is keyed by repository id, so the same task id in two repositories names two tasks.
Ids use the workflow id form: lowercase letters, digits and hyphens, starting with a letter, except for [message ids](messaging.md#envelope).

| Record | Key | Contract |
|---|---|---|
| Task | repository, task id | Title, the workflow revision (name and `sha256:` digest), the `sha256:` digest of the configuration it was accepted under, and the tasks it depends on. The revisions never change after creation (R1.62). State starts `open` and moves once to `done` or `cancelled`. Dependencies must already exist, in any repository of the same Director. |
| Stage attempt | repository, task, step, number | Numbered from 1 per task and step. Running until finished as `succeeded`, `failed` or `stopped`, once. A step cannot start a new attempt while one is running, and a task that is not open cannot start one. |
| Document reference | repository, task, name | A slash-separated relative path and the lowercase hex sha256 of the file. The document itself is a file, not a store record (R1.13). A reference never changes once recorded. |
| Pane binding | repository, task | Herdr session, workspace, tab and pane. Presentation only: binding, rebinding and unbinding never change task state (R1.52). |
| Decision | repository, task, decision id | A question for the Guildmaster and, once, the exact answer (R1.61). |
| Message | repository, message id | A message to the Director or to the Artisan of a running stage attempt, about an existing task and optionally one of its attempts, with an inbox entry until it is acknowledged once. Message ids are unique in the store. Messages never change other records; see [messaging](messaging.md). |

Titles, questions and answers must be non-blank valid UTF-8 and are limited to 64 KiB per field; larger content belongs in a referenced document.
Message payload limits and artifact requirements are defined in the [envelope contract](messaging.md#envelope).
Pane binding fields must be non-blank valid UTF-8 and are limited to 256 bytes each.
Document paths are relative to the Director's document directory and must be clean, local, slash-separated paths of at most 1024 bytes, without backslashes or colons.
Document paths must be valid UTF-8; invalid UTF-8 is refused with `ErrInvalid` before hashing or persistence.

## Mutations and receipts

Every change is a mutation that names a caller-chosen mutation id of 1 to 128 characters from letters, digits, `.`, `_`, `:` and `-`, starting with a letter or digit.
A mutation runs in one bbolt transaction that writes its records and a receipt together and commits durably before the call returns.
After a crash, either the records and the receipt are both present or neither is.

A receipt holds the mutation id, the operation, a sha256 digest of the operation and request, the repository and task, the attempt number where one applies, a store-wide sequence number starting at 1 and the commit time in UTC.
Sending message `<id>` records the mutation `message:<id>`, and acknowledging it records `message-ack:<id>`; a message's sequence is its send receipt's sequence.

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

## File format and versions

`store.Open` normally creates and initializes a missing or empty store file.
`store.Options.RequireStore` requires an already initialized Guildhall store, even for a writable open: missing, empty and non-store files are refused with `ErrNotStore` without creating or modifying them.
It checks existing metadata instead of initializing buckets, while leaving successful opens writable unless `ReadOnly` is also set.
All messaging commands use this option; `send` and `ack` cannot initialize a store as a side effect of a refused operation.

The file holds a `meta` bucket (`format = guildhall-store`, `schema_version`), a `receipts` bucket keyed by mutation id, and a `repositories` bucket with one nested bucket per repository holding `tasks`, `attempts`, `documents`, `panes`, `decisions`, `messages` and `inbox`.
`inbox` indexes unacknowledged messages by recipient and sequence, holding each message's id.
A per-repository bucket is created by the first record that needs it, so a schema 1 file written before messages existed accepts them unchanged.
Records and receipts are JSON.

Opening refuses a file that is not a Guildhall store (`ErrNotStore`), including another program's bbolt file, and a store whose `schema_version` is newer than this Guildhall supports (`ErrNewerSchema`); a refusal releases the lock.
Both writable and read-only opens refuse an existing store missing any required top-level bucket (`meta`, `receipts` or `repositories`) with `ErrNotStore`.
Required buckets are created only when initializing an empty file, never to repair an existing store.
A read-only open never creates, initializes or writes a file.

The repository holds a schema 1 store file (`internal/store/testdata/schema-v1.store`) that the tests open on Linux, Windows and macOS in CI, so every platform reads a file another platform wrote.
bbolt records the page size in the file, and every release target is little-endian.

## `guildhall store doctor`

`guildhall store doctor <path>` opens a store read-only, runs bbolt's page-level integrity check, decodes every record and prints the schema version, the receipt count and each repository's record counts, including messages and how many are unread.
It exits 0 when the store opened and showed no problems, and 1 when it is missing, locked by a writer, not a store, newer than this Guildhall or damaged.
