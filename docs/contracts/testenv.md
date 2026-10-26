# `internal/testenv`

## Contract

`SkipUnlessRequired(t, name, format, args...)` skips `t` with the formatted reason, unless `GUILDHALL_CI_REQUIRE` lists `name`; then it fails `t` with that reason and the requirement ([decision](../decisions/0046-ci-required-skips.md)).
It calls exactly one of `Skipf` and `Fatalf` on `t`, through `T`, the part of `testing.TB` it uses.
`Required` returns the listed names: comma-separated, with space around a name and empty entries ignored, compared exactly.
A name outside `Requirable` (`ksh`, `powershell`, `pwsh`, `windows`) is an error naming it, which `SkipUnlessRequired` reports as a failure, so a misspelt requirement cannot pass as an unmet one.
Unset, every such skip stays a skip, so a local run proves what its host can.

The skips that use it:

- `internal/herdr` `TestShellCommandRoundTrips`, each shell not on `PATH` (only `ksh`, `pwsh` and `powershell` are requirable);
- `skills` `TestScriptsAreTwinnedAndParse` and `TestContractTDDCheckTwinsAgree`, `pwsh` (and `sh`, which is not requirable) not on `PATH`;
- `internal/adapter` `TestOwnedWorkerStopContract` and `TestOwnedWorkerRejectsUnverifiedIdentity`, which need Windows.

The [CI workflow's matrix](../../.github/workflows/ci.yml) owns the required capabilities for each runner.

`Ceiling(d)` scales the longest a test waits for an event before it fails: `d` times `CISlowdown` (5), but no more than the larger of `d` and `CIMaxCeiling` (2 minutes), when `CI` is set to anything but empty, as on every hosted runner, and `d` unchanged otherwise ([windows-ci-speed](../decisions/0081-windows-ci-speed.md)).
The cap keeps a stuck wait failing with its message inside `go test`'s ten-minute package timeout, which otherwise stops the package and loses the test's messages.
A wait that ends on its event costs nothing extra, so a slow runner makes a test slow, not red, while a local run still fails promptly.
It is for ceilings only: a grace a test waits out on purpose, such as `internal/cli`'s `leftovers` settle for a worker it expects to survive, or a duration the code under test observes, such as a stop ladder's rungs or a stall interval, stays unscaled.
Test-binary children inherit `CI` and so the same ceilings.

`WaitFor(t, what, ready)` checks `ready` immediately, then polls until it succeeds or a CI-scaled 30-second ceiling expires, failing with the event named by `what`.
It is for real filesystem/process conditions; a producer-owned channel is preferable when available.
The [test timing policy](../test-timing.md) specifies the ten-second minimum ceiling and local `timing:allow` exceptions enforced by `TestTimingPolicy` in every CI matrix job.

## Tests

`wait_test.go` exercises immediate success, repeated condition checks and a named failure using synthetic time.
These helper checks passed on their first run; they are not historical red-first evidence.
`TestTimingPolicyRules` runs the policy scanner over accepted/rejected source fixtures, including aliases, local constants, short argv timeouts, field assignments, elapsed assertions, sleeps and explicit exceptions.
The repository-wide guard initially failed on the existing timing inventory before those sites were converted or justified.

`testenv_test.go` was written red first against a stub that always skipped: the skip and failure table over a recording `T`, `Required`'s parsing and refusal, and `TestCIRequirementsAreKnown`, which checks the running job's own `GUILDHALL_CI_REQUIRE` on every runner.
`ceiling_test.go`'s `TestCeiling` was written red first against a `Ceiling` returning `d` unchanged ([red run](../evidence/windows-ci-speed-2026-10-22-red.txt)): unset keeps the ceiling, and `true` and any other value scale it; the cap cases were added after a macOS run where `cmd/guildhall`'s scaled minute ceilings outlasted the package timeout.
The converted call sites keep their tests unchanged; run with every name required on a Linux host without `ksh` or PowerShell, each fails naming the missing tool or platform.
