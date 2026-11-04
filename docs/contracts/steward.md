# `internal/steward`

## Contract

[Rollouts, verification and writer containment](../steward.md#apply); the `Copier` and `Apply` doc comments in [`rollout.go`](../../internal/steward/rollout.go) own the consumer transport and writer-lock API contracts.
`privateLock` in [`lock.go`](../../internal/steward/lock.go) is the only taker of `.rollout.lock`, for `Apply`, `Collect` and the takeover's apply and abandon. It writes a `LockOwner` record and refuses a held lock with a `*BusyError`, which matches `ErrBusy` and `fs.ErrExist` and names the owner; it reads an unreadable record as an unknown owner and never changes or removes a lock it did not take.
`MessageDropPath`, `ReceiveCommand` and `Deliver` deliver one [Steward message](../steward.md#steward-messages): `Put` to the copy's drop file, then the fixed `messages receive --copy <dir>`, whose printed `messaging.ReceiveResult` is returned and any failure is an error; `Report`, `ReportID` and `Report.Envelope` build a `steward-report` with one id per issue and copy; `CheckCommand` admits `messages receive --copy <dir>` and no other form of it.
`StatusCommand` is the read-only `director status --json --copy <dir>`, which `CheckCommand` admits in that one form, and `ParseDirectorStatus` reads a copy's `DirectorHealth` from its output, at most `MaxStatusBytes`, ignoring fields it does not read and returning `ErrNoHealth` for a feed with no `health` ([Director health](../steward.md#director-health)).

## Tests

`messages_test.go`: `TestDeliver` copies then receives, on a Windows path, and returns delivered and refused results, and errors for a failing command, an unreadable result and an unreachable machine; `TestReportID` keeps one id per issue and copy; `ssh_test.go`'s `TestCheckCommand` and `stewardtest.RunRunnerContract` admit `messages receive --copy` and refuse its other forms.
`health_test.go`: `TestParseDirectorStatus` reads the session and health, refuses a feed with no health, output that is not JSON and an oversized feed; `TestCheckCommand` and `RunRunnerContract` admit `director status --json --copy` and refuse it without `--json`, with the flags reordered, with `--store`, with a relative copy and with an extra argument.

`stewardtest.RunCopierContract` (shared contract test) runs against the scp `Transport`, played by the test binary in a separate process (`scp_test.go`), and the in-memory `stewardtest.Fleet` that `internal/cli` tests use; `rollout_test.go`, `lock_test.go` (real files and an owner process), `roster_test.go`, `managed_test.go`
