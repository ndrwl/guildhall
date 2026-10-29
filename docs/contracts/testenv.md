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

`InstallClaudeGuard()` makes `claude` on `PATH` a guard, a shell script on Unix and `claude.cmd` on Windows, that writes `guildhall test: real claude invoked: <args>` (`ClaudeGuardMessage`) to standard error and exits 97 (`ClaudeGuardExit`), so no test reaches a real Claude Code.
It writes the guard to a new temporary directory, removes from `PATH` every directory holding another `claude` program `exec.LookPath` would find (on Windows, `claude` with a `PATHEXT` extension), and appends the guard's directory, so a stub `claudetest.Install` prepends later still wins and `claudeStubbed`'s comparison still holds.
It records the directory in `GUILDHALL_TEST_CLAUDE_GUARD`; a child that inherits both installs nothing, and its `remove` does nothing.
Removing those directories also hides any other tool beside a developer's `claude`, such as in `~/.local/bin`, from the guarded tests.
`RunWithClaudeGuard(m)` installs the guard, runs `m`, removes it and returns the exit code, 2 when the guard cannot be installed.
Every package whose tests start subprocesses calls it from `TestMain`: `cmd/guildhall`, `internal/cli`, `internal/launch`, `internal/adapter/claude` and `internal/interp`, so `go test ./...` on a developer's machine is guarded as well as CI.
This closes the gap `interp.CheckAdapter` leaves in a built binary, where `testing.Testing()` is false.

[`WaitFor`](../../internal/testenv/wait.go) supplies named condition waits for real filesystem/process events.
The [test timing policy](../test-timing.md) owns the ceiling and exception rules enforced by `TestTimingPolicy` in every CI matrix job.

## Tests

`wait_test.go` exercises immediate success, repeated condition checks and a named failure using synthetic time.
These helper checks passed on their first run; they are not historical red-first evidence.
`TestTimingPolicyRules` runs the policy scanner over accepted/rejected source fixtures, including aliases, local constants, short argv timeouts, field assignments, elapsed assertions, sleeps and explicit exceptions.
The repository-wide guard initially failed on the existing timing inventory before those sites were converted or justified.

`testenv_test.go` was written red first against a stub that always skipped: the skip and failure table over a recording `T`, `Required`'s parsing and refusal, and `TestCIRequirementsAreKnown`, which checks the running job's own `GUILDHALL_CI_REQUIRE` on every runner.
`ceiling_test.go`'s `TestCeiling` was written red first against a `Ceiling` returning `d` unchanged ([red run](../evidence/windows-ci-speed-2026-10-22-red.txt)): unset keeps the ceiling, and `true` and any other value scale it; the cap cases were added after a macOS run where `cmd/guildhall`'s scaled minute ceilings outlasted the package timeout.
`claude_guard_test.go`'s `TestClaudeGuard` was run red against an `InstallClaudeGuard` that installed nothing ([red run](../evidence/claude-guard-2026-10-29-red.txt)): with a recording `claude` earlier on `PATH`, `exec.LookPath` finds the guard, which exits 97 with its message and arguments without reaching the other; a second install keeps the first; a later stub wins.
`cmd/guildhall`'s `TestRealClaudeIsRefused` runs a plan whose profile names the claude adapter through the built binary with no stub, and wants the attempt failed by the guard; the claude wrapper's first call is its version probe, which logs the guard's exit status, not its message.
Its red run, with `TestMain` calling `m.Run()` instead of `testenv.RunWithClaudeGuard` and a recording `claude` first on `PATH`, failed because the attempt reached the recording `claude` with the Artisan's `--print` model call ([red run](../evidence/claude-guard-binary-2026-10-29-red.txt)).
The converted call sites keep their tests unchanged; run with every name required on a Linux host without `ksh` or PowerShell, each fails naming the missing tool or platform.
