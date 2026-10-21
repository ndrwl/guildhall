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
`internal/messaging` is the other: `messagingtest.RunMailboxContract` runs against the store-backed mailbox and the in-memory `messagingtest.Fake`.
Real external programs are replaced by the test binary itself acting as the program, so tests cross a real process boundary on every platform without depending on installed tools.
Integration tests against real files and processes stay alongside the doubles.
CI makes no model calls and uses no credentials or external network services.
Worker ownership tests use attempt-local loopback connections.

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
Shell round-trip tests execute installed shells against an argument-echoing child rather than replacing the shell with a fake.
The [test environment contract](contracts/testenv.md) defines which missing-tool and platform skips become failures on runners that require them.

## Current contracts

Each package's contract and its tests are in its own file under `docs/contracts/`, named after the package path without a leading `internal/` and with `/` replaced by `-`.
A pull request that adds or changes a package's contract edits only that package's file, and a new package adds its file and one line here in package order.

- [`cmd/guildhall`](contracts/cmd-guildhall.md)
- [`internal/adapter/adaptertest`](contracts/adapter-adaptertest.md)
- [`internal/adapter/fake`](contracts/adapter-fake.md)
- [`internal/adapter`](contracts/adapter.md)
- [`internal/artisan/artisantest`](contracts/artisan-artisantest.md)
- [`internal/artisan`](contracts/artisan.md)
- [`internal/atomicfile`](contracts/atomicfile.md)
- [`internal/cli`](contracts/cli.md)
- [`internal/config`](contracts/config.md)
- [`internal/digest`](contracts/digest.md)
- [`internal/git/gittest`](contracts/git-gittest.md)
- [`internal/git`](contracts/git.md)
- [`internal/herdr`](contracts/herdr.md)
- [`internal/interp/interptest`](contracts/interp-interptest.md)
- [`internal/interp`](contracts/interp.md)
- [`internal/launch`](contracts/launch.md)
- [`internal/messaging/messagingtest`](contracts/messaging-messagingtest.md)
- [`internal/messaging`](contracts/messaging.md)
- [`internal/reconcile`](contracts/reconcile.md)
- [`internal/render`](contracts/render.md)
- [`internal/steward/stewardtest`](contracts/steward-stewardtest.md)
- [`internal/steward`](contracts/steward.md)
- [`internal/store`](contracts/store.md)
- [`internal/strictjson`](contracts/strictjson.md)
- [`internal/testenv`](contracts/testenv.md)
- [`internal/workflow`](contracts/workflow.md)
- [`skills`](contracts/skills.md)
