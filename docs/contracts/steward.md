# `internal/steward`

## Contract

[Rollouts, verification and writer containment](../steward.md#apply); the `Copier` and `Apply` doc comments in [`rollout.go`](../../internal/steward/rollout.go) own the consumer transport and writer-lock API contracts.

## Tests

`stewardtest.RunCopierContract` (shared contract test) runs against the scp `Transport`, played by the test binary in a separate process (`scp_test.go`), and the in-memory `stewardtest.Fleet` that `internal/cli` tests use; `rollout_test.go`, `lock_test.go` (real files and an owner process), `roster_test.go`, `managed_test.go`
