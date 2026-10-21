# `internal/adapter`

## Contract

The spec, identity, capabilities and versioned result file, completion only from that file (`docs/adapters.md`).
`FakeName` and `ClaudeName` are the one spelling of the runtime adapter names, which [`runtimes`](adapter-runtimes.md) is keyed by and `interp.FakeAdapter` and `interp.ClaudeAdapter` refer to.
The cancel marker is the `.cancelled` sidecar beside the result file, naming the attempt: `Cancelled` reports it, refusing one that names another attempt with `ErrIdentityMismatch`, and `MarkCancelled` writes it durably and idempotently; while it is present `ReadResult` and `WriteResult` refuse with `ErrAttemptCancelled`.
The terminal boundary `tabHost` operates only on tabs the host created and honours its context; `HerdrTabs` implements it over the Herdr client.
The native worker boundary `nativeWorker` stops one verified process and proves its exit, is idempotent and concurrency-safe, refuses an ended context without terminating, and never uses a released handle; it exists only on Windows, and other hosts refuse with `ErrStopUnsupported`.

## Tests

`result_test.go`, `launch_test.go`, `run_test.go`; `tabhost_test.go` (shared contract test, `HerdrTabs` over a stub Herdr process and `fakeTabs`); `native_test.go` and `native_windows_test.go` (shared contract test, real Windows handle and `fakeNative`); `owned_test.go`, `owned_contract_test.go`, `cancel_test.go`; `Cancelled` and `MarkCancelled` wrap the existing sidecar functions and are exercised by the [launch tests](launch.md#tests); `conformance_test.go` (scripted runtime double, with bare prompts and on interpreter-rendered attempts mapped through `interp.Spec.AdapterSpec`); `export_test.go` exposes the `tabHost` contract test as `RunTabHostContract`, which `shellpanes_unix_test.go` runs against `adaptertest.ShellPanes`
