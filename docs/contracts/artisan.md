# `internal/artisan`

## Contract

Process identity: `Lookup` and `Probe` by PID plus a boot-scoped creation token; a zombie is gone, a failed probe is unverified, never gone.
The containment seam the launcher consumes, `Unit`: start with verified membership, members by identity, interrupt, terminate (Unix only, `ErrUnsupportedRung` on Windows), kill, exited and close; a stop rung counts only when the unit is empty.
Each unit reports its `Containment` (kind, name, Linux detection reason), and `RecordContainment` writes it to the attempt directory as `containment.json`.
See [Artisan containment](../artisan.md#containment-unit) for the backends and Linux detection rules.
The control endpoint ([attempt-endpoint-af-unix](../decisions/attempt-endpoint-af-unix.md)): `EndpointPath` with the hashed fallback and `ErrEndpointPath`; `Listen` with its `OwnerLookup` seam, `ErrNotOwner`, and the stale-file rule (`ErrEndpointAnswers`, `ErrEndpointUnknown`); the `EndpointHandler` the launcher supplies for `status`, `stop` and `cancel`; `Close`; and `Call` with `ErrEndpointMismatch` and `ErrEndpointRequest`.

## Tests

`TestRecordContainment` was written with the function and passed on first run; it did not precede implementation.
The review retrofit is explicitly authorized.
Its atomic-replacement assertion keeps a hard link to the previous record and requires that record to remain unchanged after replacement.
Behavioral failure evidence is checked by running this test against isolated Go overlays with (1) the JSON key changed from `containment` to `kind`, and (2) a direct destination write instead of temporary-file rename.
These mutations must fail the key and prior-record assertions respectively; this is retrofit evidence, not a claim of test-first development.

`unit_test.go` runs the shared suite against the platform backend on each runner; `unit_linux_test.go` runs it against the cgroup backend (skipped with the detection reason when no cgroup v2 is delegated) and the subreaper fallback, and checks that an orphaned escape is reported by `Escapes`; `cgroup_internal_linux_test.go` (the detection table, stale nonmember PIDs, refused names); `cgroup_linux_test.go` (`cgroup.kill` emptying the unit after a test-binary grandchild acknowledges setsid and its separate process group is checked, and `Close` removing the cgroup); `containment_test.go` (the attempt-directory record and atomic replacement); `fakeunit_test.go` runs it against `fakeUnit`, the launcher's double; `unit_unix_internal_test.go` (leader check, close retry after a refused kill, start rollback killing descendants); `unit_internal_windows_test.go` (a process outside the job is not a member; interrupt after worker exit and retained-handle cleanup); `unit_internal_linux_test.go` (failed exec restores foreground ownership using a controlling pseudo-terminal); `identity_test.go` (real processes); `identity_windows_test.go` (exit code 259 with a retained process handle).

`endpoint_test.go` binds real AF_UNIX sockets in a short temporary state directory on each runner, with the test binary playing an unrecorded process that holds a socket; `endpoint_linux_test.go` keeps a socket that refuses access.
Written red first against stubs: the path and its limit boundary with the hashed fallback, bind-after-own ordering (no record, another launcher, then the recorded owner), the stale-file rule (a killed process's socket and a closed listener's socket removed; an answering socket, a directory and an unreachable socket refused and kept), dispatch of `status`, `stop` and `cancel`, handler errors, attempt and launcher mismatch, status answered during a blocked stop and the caller's deadline, and `TestEndpointWithStoreOwnership` against a real store record (refused before ownership and while another launcher owns the attempt, bound after `TakeOwnership`, recorded by `UpdateOwnership`, refused once `FinishAttempt` releases it).
`TestEndpointLineProtocol` pins the scratch launcher's line protocol and its malformed-request reply, and the stale-socket and answering-socket cases pin scratch L21.
