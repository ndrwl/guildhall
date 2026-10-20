# `internal/artisan`

## Contract

Process identity: `Lookup` and `Probe` by PID plus a boot-scoped creation token; a zombie is gone, a failed probe is unverified, never gone.
The containment seam the launcher consumes, `Unit`: start with verified membership, members by identity, interrupt, terminate (Unix only, `ErrUnsupportedRung` on Windows), kill, exited and close; a stop rung counts only when the unit is empty.
Backends: a Windows Job Object and a Unix process group with the leader check (`docs/artisan.md`).

## Tests

`unit_test.go` runs the shared suite against the platform backend on each runner; `fakeunit_test.go` runs it against `fakeUnit`, the launcher's double; `unit_unix_internal_test.go` (leader check, close retry after a refused kill, start rollback killing descendants); `unit_internal_windows_test.go` (a process outside the job is not a member); `identity_test.go` (real processes)
