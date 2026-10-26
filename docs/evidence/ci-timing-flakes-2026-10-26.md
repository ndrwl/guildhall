# Timing-flake regression evidence — 2026-10-26

These regressions were written before their product fixes.
The broader test changes retrofit existing behavior; they are not claimed as historical test-first development.
All local executions below used Linux and no race detector.
No live terminal lifecycle or model-call test was enabled.

## Result timeout charged to ownership

```text
go test ./internal/cli -run 'TestArtisan(ResultTimeoutDoesNotBoundOwnership|StopProvesAPreStartCancellation)$' -count=1

--- FAIL: TestArtisanResultTimeoutDoesNotBoundOwnership
start: exit 1 ... guildhall: launch: take ownership of app/t-1/build/1: context deadline exceeded
```

After separating the result timer from setup, the same command passed.
The regression uses immediate result expiry and requires a recorded worker identity, a stopped outcome and released ownership.
The ordinary pre-start CLI case passed first on this host because its named cgroup already supplied empty-unit evidence.

## Pre-start cancellation without named containment

```text
go test ./internal/launch -run '^TestStopProvesPreStartCancellationWithoutNamedContainment$' -count=1

--- FAIL: TestStopProvesPreStartCancellationWithoutNamedContainment
stop after launcher exited = {Endpoint:not asked: ownership released Classification:{State:cancelled ...} Launcher:gone}, attempt is not proven stopped; want proven stopped
```

After recording explicit completed-no-start evidence, the same command passed.
The test uses a real unit and child launcher with no recoverable unit description before startup, matching the process-group boundary on every host.
Crash-window refusal tests remain intact.

## Cancelled ownership wait accepting readiness

```text
go test ./internal/launch -run '^TestOwnedWaitHonoursCancellationBeforeReady$' -count=1

--- FAIL: TestOwnedWaitHonoursCancellationBeforeReady
owned after cancellation = <nil>, want cancellation
```

After checking cancellation before accepting readiness, this regression passed.
A second subcase held the store writer, waited for the ownership read to block using synthetic time, then cancelled and released the writer.
It reproduced the same failure when cancellation was checked only before the read:

```text
go test ./internal/launch -run '^TestOwnedWaitHonoursCancellationBeforeReady$/during_read' -count=1
--- FAIL: TestOwnedWaitHonoursCancellationBeforeReady/during_read
owned after cancellation = <nil>, want cancellation
```

Checking cancellation again after the blocking read made both subcases pass 100 repetitions (`3.446s`).
The stopped-launcher watcher test can now cancel at an explicit setup boundary instead of relying on a one-millisecond startup race.

## Repetition and suite checks

```text
go test ./internal/cli -run 'TestTheWatcherReportsAnInterruptedLaunch|TestArtisanStartOwnsTheAttemptAndExitsByOutcome|TestArtisanResultTimeoutDoesNotBoundOwnership|TestArtisanStopProvesAPreStartCancellation' -count=100
ok github.com/ndrwl/guildhall/internal/cli 85.907s

go test ./internal/launch -run 'TestStopProvesPreStartCancellationWithoutNamedContainment|TestOwnedWaitHonoursCancellationBeforeReady|TestProcessLaunchReturnsOnceItsStoppedLauncherReleases' -count=100
ok github.com/ndrwl/guildhall/internal/launch 22.682s

go test ./internal/artisan -run 'TestLauncherCancelBeforeAutomaticSignal|TestLauncherCancelledDescendantsRequireStop' -count=100
ok github.com/ndrwl/guildhall/internal/artisan 0.732s
```

The uncached `go test ./... -count=1` run passed, including the timing-policy guard.
The final Artisan-only clock/ladder adjustment also passed its full package suite.
The optional workspace-ordering timeout test passed ten repetitions (`20.729s`); it checks the timeout reason and preserved caller budget, not whether the child started before the short product budget expired.
`go build ./...`, `go vet ./...`, formatting, and the CI-pinned `golangci-lint` 2.15.0 passed locally.
Windows and macOS test binaries cross-compiled with `CGO_ENABLED=0`; this is compile evidence only, not native test execution.
Three-OS native CI is still required before delivery is declared CI-ready.

The inventory included direct timing expressions and the test/helper call closure, including timestamp fixtures and tests inheriting only cleanup ceilings.
The initial repository-wide timing guard failed on existing short limits and standalone sleeps; it passes after the conversions and locally justified exceptions.
The scanner's policy fixtures passed on their first run and are not represented as a red-first implementation history.
