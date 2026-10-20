# `internal/interp`

## Contract

The launch seam the interpreter consumes ([scripted mock agent](../mock-agent.md#the-launch-seam)): `Launcher`, `Spec` and its validation, `Handle`, and the `CheckAdapter` guard that refuses every adapter but `mock` and `fake` inside a test binary.
`BlockedQuestion` is the provisional file a blocked attempt writes.

## Tests

`launcher_test.go` (the guard inside and outside a test binary); `Spec.Validate` through the shared suite in `interptest`
