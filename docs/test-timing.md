# Test timing policy

A test passes because an event happened or a state was observed, not because the host scheduled it within a small wall-clock window.
Real process, socket, file and store integration tests remain part of the suite on Linux, Windows and macOS.
They use no model calls or credentials.

## Choose the clock and synchronization boundary

- Use `testing/synctest` for timer-driven in-process code whose goroutines can quiesce without external processes or signal delivery.
  The adapter result waiter, atomic-file retry, store-reader contention and cleanup-rule tests use this clock.
  Windows exclusive-handle retry tests use real handles inside the synthetic-clock bubble: the open fails synchronously while held, and retry and release timers advance together.
- Use an existing injected clock or sleeper when available.
  Reconcile tests advance `Options.Now` against a frozen heartbeat; fake Artisan tests inspect the work duration requested through their existing sleeper boundary.
- Use channels for acknowledgements and cancellation ordering.
  A Claude stop test waits for the stub's startup record before cancelling, and endpoint tests cancel only after the handler acknowledges entry.
- Poll an observable cross-process condition within a generous ceiling when the producer cannot send a channel event.
  Use [`testenv.WaitFor`](../internal/testenv/wait.go) for a named condition wait.
  Existing condition loops may retain their own generous ceiling.
  A polling interval controls cost, not success.
- Check ordering and durable state rather than elapsed duration.
  The launcher's advance waits on a release file which is written only after `Launch` returns; stall tests observe later heartbeat sequences rather than assuming several beats fit in a sleep.

A real-time ceiling must be at least ten seconds before `testenv.Ceiling` scaling, normally 30 seconds or a minute for processes.
Real containment test ladders allow ten seconds per rung and their enclosing waits allow the whole ladder.
A successful event-driven wait returns immediately; increasing its failure ceiling does not slow the happy path.
Do not increase an intentional observation interval as though it were a completion ceiling.

## CI guard

Run:

```sh
go test ./internal/testenv -run '^TestTimingPolicy' -count=1
```

The ordinary suite also runs the guard, and the CI matrix has an explicit timing-policy step on all three operating systems.
It parses test files and shared helper packages, including ordinary Go files importing `testing`, excluding `testdata` fixtures.
It flags short timer/context deadlines, `time.Now().Add` deadlines, timeout and grace options/assignments, literal `--timeout` arguments, elapsed-time comparisons, and sleeps outside condition-loop post clauses.
It resolves file-local constants, local duration bindings, time-import aliases and `Ceiling` wrappers.
A `synctest.Test` body is synthetic time, not a wall-clock limit.

An intentional exception needs a local Go comment, on the statement or immediately preceding line:

```go
// timing:allow intentional expiry while an acknowledged lock is held; no progress must fit this budget.
ctx, cancel := context.WithTimeout(parent, 100*time.Millisecond)
```

Explain why runner speed cannot change the expected result.
Appropriate exceptions include deliberate timeout/refusal inputs, polling cadence inside a separately bounded loop, intentionally hung subprocess fixtures, and negative observations while an acknowledged barrier remains closed.
A short positive startup/completion deadline is not an exception.
The guard tests its own policy against executable parser fixtures, including rejected and accepted cases.
It is a static lint rule, not whole-program dataflow analysis: dynamic durations, cross-file aliases and implicit production defaults still require review.
Do not hide a budget in a helper to evade it.

## Ownership regressions

See [Artisan commands](artisan.md#commands) for the owned result-timeout lifecycle, the [launch contract](contracts/launch.md) for ownership-wait cancellation, and the [store containment record](store.md#containment-record) for pre-start recovery evidence.
The [decision](decisions/ci-timing-flakes.md) records the rationale; [regression evidence](evidence/ci-timing-flakes-2026-10-26.md) captures the failures and subsequent runs.
