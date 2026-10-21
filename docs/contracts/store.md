# `internal/store`

## Contract

`Open`, locking, mutation ids and receipts, and the records, messages, inboxes and workflow approvals (`docs/store.md`, D9, D10, D15, D22), including typed decisions bound to a subject with option-checked answers (D23), Director rulings recorded once and contested once with the review projection on document references ([director-rulings](../decisions/director-rulings.md)), and receipts naming their record with `TaskHistory`'s one-transaction task read (D24).
Attempt ownership ([store-attempt-ownership](../decisions/store-attempt-ownership.md)): `Ownership`, `StartOwnedAttempt`, `TakeOwnership` with its `OwnerProbe` seam and `ErrOwnerLive`/`ErrOwnerUnknown` refusals, `UpdateOwnership`, `Ownership`, and release by `FinishAttempt`.
The ownership record's `Containment` is a recovery description of every Artisan containment kind ([containment-persistence](../decisions/containment-persistence.md)): `Containment.Recoverable` and `ErrUnrecoverable`, the kind constants, a worker identity in place of a name for process groups, the Linux detection reason, and legacy records read unchanged but never recoverable when incomplete.

The [attempt ownership contract](../store.md#attempt-ownership) defines the default probe's containment refusal and the explicit containment-aware recovery path.

`PutHeartbeat` records fixture heartbeat evidence for a running attempt: positive increasing sequence, nonzero time and last-output time, last output no later than heartbeat time, and nonblank bounded worker state. Missing attempts return `ErrNotFound`; completed attempts and non-increasing sequence return `ErrInvalid`. Mutation replay returns its receipt unchanged. `Heartbeat` reads the latest evidence, returning `ErrNotFound` if absent. The interpreter only reads it; launcher L6 will own production writes.

## Tests

Real bbolt files: `store_test.go`, `records_test.go`, `messages_test.go`, `approvals_test.go`, `history_test.go`, `ownership_test.go`, `process_test.go` (separate processes, including a take-over refused while another process owns the attempt and allowed once it is killed), `fixture_test.go` (schema v1 file), `rulings_test.go`

`rulings_test.go` was written red first against stubbed `RuleFinding` and `ContestRuling`: each refusal (unknown move, blank reason, preset contest, deferring a rule-cited finding, unknown finding, document or task, a document without a projection), replay, a second ruling, contesting once, `TaskHistory`'s rulings and the projection's validation.
The ownership tests were written red first against stubbed methods: refusing a live owner, refusing an unknown owner, allowing a take-over after proven exit, release by `FinishAttempt`, replay by mutation id, the second-process refusal, one-mutation start with crash rollback, validation, one winner among concurrent take-overs, and doctor counts.
The containment record tests were written red first against a stubbed `Recoverable`: named and nameless round trips (`TestContainmentRoundTrips`), refusal of incomplete evidence (`TestContainmentRequiresRecoveryEvidence`), and a legacy record that stays readable while its incomplete group evidence refuses a take-over (`TestLegacyContainmentRecords`).
`TestUpdateOwnership` and `TestOwnershipValidates`'s two existing containment cases are retrofit pins that still hold for named kinds.
`TestTakeOwnershipAfterProvenExit` recorded a process group by its group id as a name; that expectation was wrong under this contract, and the test now records the group by its worker identity.
`TestTakeOwnershipPreservesContainmentAfterLauncherExit` pins refusal and intact persisted evidence for every containment kind with a dead launcher and a surviving worker, through both nil and explicit `ProbeLauncher` probes, followed by explicit recovery after worker exit.
`TestProbeLauncherAgainstRealProcesses` pins the scratch launcher's owner cases (a live owner refused; a reused PID with another token taken over).
The heartbeat case of `interptest.RunRecordsContract` runs against the real store and the double. It was written before the heartbeat implementation (new red-first intent); execution is deferred to the fix round's final focused verification, not claimed as an observed red run.
