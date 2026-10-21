# `internal/testenv`

## Contract

`SkipUnlessRequired(t, name, format, args...)` skips `t` with the formatted reason, unless `GUILDHALL_CI_REQUIRE` lists `name`; then it fails `t` with that reason and the requirement ([decision](../decisions/ci-required-skips.md)).
It calls exactly one of `Skipf` and `Fatalf` on `t`, through `T`, the part of `testing.TB` it uses.
`Required` returns the listed names: comma-separated, with space around a name and empty entries ignored, compared exactly.
A name outside `Requirable` (`ksh`, `powershell`, `pwsh`, `windows`) is an error naming it, which `SkipUnlessRequired` reports as a failure, so a misspelt requirement cannot pass as an unmet one.
Unset, every such skip stays a skip, so a local run proves what its host can.

The skips that use it:

- `internal/herdr` `TestShellCommandRoundTrips`, each shell not on `PATH` (only `ksh`, `pwsh` and `powershell` are requirable);
- `skills` `TestScriptsAreTwinnedAndParse` and `TestContractTDDCheckTwinsAgree`, `pwsh` (and `sh`, which is not requirable) not on `PATH`;
- `internal/adapter` `TestOwnedWorkerStopContract` and `TestOwnedWorkerRejectsUnverifiedIdentity`, which need Windows.

The [CI workflow's matrix](../../.github/workflows/ci.yml) owns the required capabilities for each runner.

## Tests

`testenv_test.go` was written red first against a stub that always skipped: the skip and failure table over a recording `T`, `Required`'s parsing and refusal, and `TestCIRequirementsAreKnown`, which checks the running job's own `GUILDHALL_CI_REQUIRE` on every runner.
The converted call sites keep their tests unchanged; run with every name required on a Linux host without `ksh` or PowerShell, each fails naming the missing tool or platform.
