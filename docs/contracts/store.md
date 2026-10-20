# `internal/store`

## Contract

`Open`, locking, mutation ids and receipts, and the records, messages, inboxes and workflow approvals (`docs/store.md`, D9, D10, D15, D22), including typed decisions bound to a subject with option-checked answers (D23), and receipts naming their record with `TaskHistory`'s one-transaction task read (D24).
Attempt ownership ([store-attempt-ownership](../decisions/store-attempt-ownership.md)): `Ownership`, `StartOwnedAttempt`, `TakeOwnership` with its `OwnerProbe` seam and `ErrOwnerLive`/`ErrOwnerUnknown` refusals, `UpdateOwnership`, `Ownership`, and release by `FinishAttempt`.

## Tests

Real bbolt files: `store_test.go`, `records_test.go`, `messages_test.go`, `approvals_test.go`, `history_test.go`, `ownership_test.go`, `process_test.go` (separate processes, including a take-over refused while another process owns the attempt and allowed once it is killed), `fixture_test.go` (schema v1 file)

The ownership tests were written red first against stubbed methods: refusing a live owner, refusing an unknown owner, allowing a take-over after proven exit, release by `FinishAttempt`, replay by mutation id, the second-process refusal, one-mutation start with crash rollback, validation, one winner among concurrent take-overs, and doctor counts.
`TestProbeLauncherAgainstRealProcesses` pins the scratch launcher's owner cases (a live owner refused; a reused PID with another token taken over).
The interpreter's `interp.Records` interface does not exist yet; when slice S6a declares it, it gains the ownership methods and its double runs against these behaviours.
