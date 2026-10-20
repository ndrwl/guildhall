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
Not every current double meets this yet; see [outstanding retrofit](#outstanding-retrofit).
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
Shell round-trip tests execute installed shells against an argument-echoing child and skip missing shells by name rather than replacing the shell with a fake.

## Current contracts

| Package | Contract | Tests |
|---|---|---|
| `cmd/guildhall` | The process entry point: arguments, standard streams and exit status are handed to `cli.App.Run`. | `main_test.go` builds the binary with the release version linker flag and runs `version`, `doctor` and an unknown command |
| `internal/cli` | Commands, output and exit codes (README); doctor's process boundary, `App.LookPath` and `App.Output`: a missing tool is `exec.ErrNotFound`, output is stdout and stderr combined, a failing command is an error with its output, and context cancellation stops a hung command; see D6 for doctor's timeout policy. The `store`, `messages`, `task`, `workflow approve|approvals` and `steward rollout` commands (`docs/store.md`, `docs/messaging.md`, `docs/tasks.md`, `docs/configuration.md`, `docs/steward.md`); see `docs/tasks.md` for task acceptance and retry contracts. | `cli_test.go`, `tools_test.go` (shared contract test, real and double); real store files: `store_test.go`, `messages_test.go`, `task_test.go` (with `testdata/` goldens, real config and workflow files, and `task_windows_test.go` on the Windows runner), `workflow_test.go` (with a `testdata/` golden); `steward_test.go` (in-process scp double, see below) |
| `internal/config` | `Parse` refuses unknown keys, newer schemas and credentials, applies defaults and validates (`docs/configuration.md`). Repository paths follow the owner-machine contract (D11): POSIX-rooted and rooted Windows drive-letter paths are accepted on every host; relative, drive-relative and backslash-prefixed UNC paths are refused. Duplicate comparison normalizes dot segments and, for drive paths, separators and drive-letter case independently of the parsing host. `EncodeManaged` and `ParseCombined` frame and verify the separate managed file and refuse inline managed settings beside it (D14). `Load` follows the filesystem fallback and error rules in [configuration](configuration.md). | `config_test.go`, `managed_test.go`; real temporary files: `load_test.go` |
| `internal/workflow` | `Parse`, `Validate`, the step registry, overrides and digests, and the review outcome shared by human and automated review (R1.60). | `workflow_test.go`, `strict_test.go`, `review_test.go` |
| `internal/store` | `Open`, locking, mutation ids and receipts, and the records, messages, inboxes and workflow approvals (`docs/store.md`, D9, D10, D15, D22). | Real bbolt files: `store_test.go`, `records_test.go`, `messages_test.go`, `approvals_test.go`, `process_test.go` (separate processes), `fixture_test.go` (schema v1 file) |
| `internal/messaging` | `Decode` and the envelope (`docs/messaging.md`); `Mailbox`: durable send with receipts, retry and id-reuse conflict, recipient, task and attempt checks, bounded ordered listing and acknowledgement, concurrent use (D15). `StoreMailbox` implements it on the store. | `envelope_test.go` (with `testdata/` encodings); `store_mailbox_test.go` runs the shared suite against real store files, closing and reopening them for restart |
| `internal/messaging/messagingtest` | The shared `Mailbox` contract suite, `RunMailboxContract` and its `Harness`, and `Fake`, an in-memory mailbox for consumer tests that passes it. The fake's restart keeps its state, so it models a durable mailbox rather than proving durability. | `fake_test.go` runs the suite against `Fake` |
| `internal/steward` | Roster and managed-source parsing, dry-run plans and previews, apply with revalidation, approval, revisions, read-back verification and receipts, and the private config directory's apply lock (`docs/steward.md`, D12, D13, D14). `Transport` runs only `scp` as an argument list. | `roster_test.go`, `managed_test.go`; `rollout_test.go` against real files, with `stub_test.go` re-executing the test binary as `scp` |
| `internal/herdr` | `Client` names an explicit non-default session on every call and operates only on tabs it created (`docs/adapters.md`); its process boundary, `Client.run`, passes argv and env exactly, keeps stdout and stderr apart, reports a nonzero exit with its output, errors when a program cannot start, and reports cancellation as the context's error. `ShellCommand` renders argv so the named shell starts exactly that argv, or refuses (D20). | `runner_test.go` (shared contract test, real `execRun` and `fakeRunner`), `herdr_test.go` (client against recorded Herdr 0.9.5 responses), `shell_test.go`, `shell_exec_test.go` (round trips executed in each installed shell) |
| `internal/adapter` | The spec, identity, capabilities and versioned result file, completion only from that file (`docs/adapters.md`). The terminal boundary `tabHost` operates only on tabs the host created and honours its context; `HerdrTabs` implements it over the Herdr client. The native worker boundary `nativeWorker` stops one verified process and proves its exit, is idempotent and concurrency-safe, refuses an ended context without terminating, and never uses a released handle; it exists only on Windows, and other hosts refuse with `ErrStopUnsupported`. | `result_test.go`, `launch_test.go`, `run_test.go`; `tabhost_test.go` (shared contract test, `HerdrTabs` over a stub Herdr process and `fakeTabs`); `native_test.go` and `native_windows_test.go` (shared contract test, real Windows handle and `fakeNative`); `owned_test.go`, `owned_contract_test.go`, `cancel_test.go`; `conformance_test.go` (scripted runtime double) |
| `internal/adapter/adaptertest` | `RunConformance`: the launch and result protocol every runtime adapter passes through a real child process, with no model calls; `Runtime` is the narrow interface it consumes. | Run by `internal/adapter/fake` and `internal/adapter` |
| `internal/adapter/fake` | The test-only fake runtime: no optional capabilities, bounded prompt directives (`docs/adapters.md`). | `fake_test.go`, `conformance_test.go` (the shipped fake as a real child process) |

## Outstanding retrofit

These contracts are written down and partly tested, but their tests do not yet meet this page.
They are tracked work, not exceptions.

- Steward transport: `Transport` has no narrow consumer interface and no shared contract test; `internal/cli/steward_test.go` answers scp with its own in-process double that no test checks against the real transport.
  The retrofit adds the consumer interface, one suite run against the scp stub and an in-memory double, and moves the CLI tests onto that double.
- Steward apply lock: refusal of an existing lock is tested, but contention, release after success and failure, and manual recovery after the owner stops have no focused tests.
- The Steward retrofit is owned by its own change; this page is updated when it lands.
