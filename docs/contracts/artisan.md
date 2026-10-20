# `internal/artisan`

## Contract

Process identity: `Lookup` and `Probe` by PID plus a boot-scoped creation token; a zombie is gone, a failed probe is unverified, never gone.
The containment seam the launcher consumes, `Unit`: start with verified membership, members by identity, interrupt, terminate (Unix only, `ErrUnsupportedRung` on Windows), kill, exited and close; a stop rung counts only when the unit is empty.
Each unit reports its `Containment` (kind, name, Linux detection reason), and `RecordContainment` writes it to the attempt directory as `containment.json`.
Backends: a Windows Job Object; on Linux a child cgroup of a delegated cgroup v2 (members from `cgroup.procs`, kill through `cgroup.kill`), otherwise a process group with the launcher as child subreaper, whose orphaned escapes `Escapes` reports; a macOS process group with the leader check (`docs/artisan.md`).

## Tests

`unit_test.go` runs the shared suite against the platform backend on each runner; `unit_linux_test.go` runs it against the cgroup backend (skipped with the detection reason when no cgroup v2 is delegated) and the subreaper fallback, and checks that an orphaned escape is reported by `Escapes`; `cgroup_internal_linux_test.go` (the detection table, `cgroup.kill` emptying the unit and `Close` removing the cgroup, refused names); `containment_test.go` (the attempt-directory record); `fakeunit_test.go` runs it against `fakeUnit`, the launcher's double; `unit_unix_internal_test.go` (leader check, close retry after a refused kill, start rollback killing descendants); `unit_internal_windows_test.go` (a process outside the job is not a member; interrupt after worker exit and retained-handle cleanup); `unit_internal_linux_test.go` (failed exec restores foreground ownership using a controlling pseudo-terminal); `identity_test.go` (real processes); `identity_windows_test.go` (exit code 259 with a retained process handle).
