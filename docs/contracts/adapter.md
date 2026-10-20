# `internal/adapter`

## Contract

The spec, identity, capabilities and versioned result file, completion only from that file (`docs/adapters.md`).
The terminal boundary `tabHost` operates only on tabs the host created and honours its context; `HerdrTabs` implements it over the Herdr client.
The native worker boundary `nativeWorker` stops one verified process and proves its exit, is idempotent and concurrency-safe, refuses an ended context without terminating, and never uses a released handle; it exists only on Windows, and other hosts refuse with `ErrStopUnsupported`.

## Tests

`result_test.go`, `launch_test.go`, `run_test.go`; `tabhost_test.go` (shared contract test, `HerdrTabs` over a stub Herdr process and `fakeTabs`); `native_test.go` and `native_windows_test.go` (shared contract test, real Windows handle and `fakeNative`); `owned_test.go`, `owned_contract_test.go`, `cancel_test.go`; `conformance_test.go` (scripted runtime double)
