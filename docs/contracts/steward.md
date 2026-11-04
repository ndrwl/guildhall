# `internal/steward`

## Contract

[Rollouts, verification and writer containment](../steward.md#apply); the `Copier` and `Apply` doc comments in [`rollout.go`](../../internal/steward/rollout.go) own the consumer transport and writer-lock API contracts.
`MessageDropPath`, `ReceiveCommand` and `Deliver` deliver one [Steward message](../steward.md#steward-messages): `Put` to the copy's drop file, then the fixed `messages receive --copy <dir>`, whose printed `messaging.ReceiveResult` is returned and any failure is an error; `Report`, `ReportID` and `Report.Envelope` build a `steward-report` with one id per issue and copy; `CheckCommand` admits `messages receive --copy <dir>` and no other form of it.

## Tests

`messages_test.go`: `TestDeliver` copies then receives, on a Windows path, and returns delivered and refused results, and errors for a failing command, an unreadable result and an unreachable machine; `TestReportID` keeps one id per issue and copy; `ssh_test.go`'s `TestCheckCommand` and `stewardtest.RunRunnerContract` admit `messages receive --copy` and refuse its other forms.

`stewardtest.RunCopierContract` (shared contract test) runs against the scp `Transport`, played by the test binary in a separate process (`scp_test.go`), and the in-memory `stewardtest.Fleet` that `internal/cli` tests use; `rollout_test.go`, `lock_test.go` (real files and an owner process), `roster_test.go`, `managed_test.go`
