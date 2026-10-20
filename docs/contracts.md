# Contracts and tests

Guildhall is built contract first.
This page says what that means for a change, which boundaries get an interface, how packages and tests are laid out, and which contracts exist today.

## Method

1. State the contract before the implementation: the operations, inputs and outputs, errors, and where they apply, concurrency, idempotency, identity, ownership and recovery.
   The contract lives in the doc comments of the exported types and functions and, for a module with user-visible behaviour, in its page under `docs/`.
2. Write tests against that contract and see them fail.
3. Implement until the tests pass.
   A test is changed only when it contradicts an accepted requirement or decision, and the pull request cites that evidence; a test is never weakened to make an implementation pass.

Existing code is retrofitted the same way: its contract is written down and tested, and the pull request says which tests are new and which behaviour they pin.
A new test that passes on first run against existing behaviour is reported as such, not as red-then-green evidence.

## Which boundaries get an interface

An interface or function field marks a real seam: something with more than one source of behaviour, such as an external process, the operating system, a terminal, a shell, an agent runtime or a remote host.
The consumer declares the narrow set of operations it calls, next to the code that calls them; the implementation returns concrete types.

These do not get an interface:

- pure parsing, validation and value types, which are functions with behavioural tests;
- the store as a whole: it has no generic storage interface (D9), and its tests use real database files, including crash, replay and cross-process locking behaviour; a consumer that needs to replace it in tests declares a narrow interface over just the methods it calls;
- anything with a single implementation and no test that needs to replace it.

## Test doubles and shared contract tests

When a test double stands in for a real boundary, one contract test runs against both, so a test that passes against the double means the same against the real implementation.
`internal/cli/tools_test.go` is the example: `testToolBoundary` runs against the process boundary `cli.New` wires and against the `fakeTools` double.
Real external programs are replaced by the test binary itself acting as the program, so tests cross a real process boundary on every platform without depending on installed tools.
Integration tests against real files and processes stay alongside the doubles.
CI makes no model calls and uses no credentials or network.

## Layout

- One package per domain under `internal/`, such as `config`, `workflow` and `store`; no global `interfaces`, `implementations` or `tests` trees.
- The package's contract (types, errors, package documentation) is in the file named after the package; an implementation of an external seam is in a file named after what it wraps.
- Tests sit beside the code as `*_test.go`, with fixtures in `testdata/`.
- Test helpers and doubles stay in `*_test.go` files; a `<package>test` package is added only when tests in another package need them.
- A package whose implementations are genuinely separate, such as runtime adapters, may keep each implementation in its own subpackage beside the contract.

## Platforms and shells

A platform difference is part of the contract: say what differs, and test each side on its native CI runner.
Put platform-specific code or test cases in `_windows.go` files or files with a `//go:build` line, rather than branching on `runtime.GOOS` throughout.
Where a platform or shell cannot do something, refuse it with an error that names the limit rather than emulating it.
Run programs with `os/exec` argument lists; where text must cross an interactive shell, quote it per shell and refuse what that shell cannot pass unchanged.

## Current contracts

| Package | Contract | Tests |
|---|---|---|
| `internal/cli` | Commands, output and exit codes (README); doctor's process boundary, `App.LookPath` and `App.Output`: a missing tool is `exec.ErrNotFound`, output is stdout and stderr combined, a failing command is an error with its output, and context cancellation stops a hung command; see D6 for doctor's timeout policy. | `cli_test.go`, `store_test.go`, `tools_test.go` (shared contract test, real and double) |
| `internal/config` | `Parse` refuses unknown keys, newer schemas and credentials, applies defaults and validates (`docs/configuration.md`). Repository paths are checked as absolute by the parsing host's rules. | `config_test.go`, with per-platform path cases in `paths_windows_test.go` and `paths_other_test.go` |
| `internal/workflow` | `Parse`, `Validate`, the step registry, overrides and digests, and the review outcome shared by human and automated review (R1.60). | `workflow_test.go`, `strict_test.go`, `review_test.go` |
| `internal/store` | `Open`, locking, mutation ids and receipts, and the records (`docs/store.md`). | Real bbolt files: `store_test.go`, `records_test.go`, `process_test.go` (separate processes), `fixture_test.go` (schema v1 file) |
