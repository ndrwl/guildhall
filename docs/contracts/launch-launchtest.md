# `internal/launch/launchtest`

## Contract

The one test cleanup rule for an owned attempt, over [`launch.Stop`](launch.md)'s report as `artisan stop` prints it ([commands](../artisan.md#commands)).
`Stop` runs a `Stopper`, one `artisan stop` of the attempt, until its report proves the launcher no longer runs: a stop that exits 0 without the endpoint's answer (no ownership record, a launcher gone after its endpoint failed, or a released ownership whose launcher is gone and unit proven empty), or a released ownership whose `launcher` is `gone` and whose classification is `exited`, released at setup before any containment was recorded.
A stop the endpoint answered leaves the launcher to record the outcome and run the task's advance, so it is run again until the launcher is gone; a `launcher: advancing` report is retried.
A stop that fails once its report shows the launcher gone is run once more, since the launcher may have exited while the stop probed it, and its error is then returned at once, so a surviving worker is reported rather than waited out.
Otherwise it retries within `Bound`, the watcher's worst case: two store lock waits (`LockWait`, the CLI's `storeWait`) of the advance and its one retry, plus `StopTimeout`, `artisan stop`'s ladder wait (`defaultStopTimeout`), scaled by `testenv.Ceiling`; past it, the last stop's error with its report.
`StopAll` applies `Stop` to each attempt key and fails the test once for each it cannot prove; `Report` reads one run's printed report and error.
Consumers: the `testdata/run` cleanup in `internal/cli` (`leftovers`), and `binDirector.stopAll` and the safety checks' `stopAttempt` in `cmd/guildhall`.

## Tests

`launchtest_test.go` was written red first against stub bodies ([red run](../evidence/stop-cleanup-2026-10-22-red.txt)): over a scripted stopper, no ownership record and a launcher gone after its endpoint failed are proven at the first stop; an answered stop is run again through `advancing` reports until the launcher is gone; a launcher seen gone is stopped once more; a launcher released at setup is proven by `gone` and `exited`; a gone launcher whose unit is not proven empty, and a killed launcher whose worker survives, fail after one more stop; a launcher that keeps advancing, and an unverified one, fail at the bound; `StopAll` fails once per unproven attempt naming it; `Bound` is two lock waits and the ladder wait.
`internal/cli`'s `TestCleanupBoundFollowsTheWatcher` pins `LockWait` and `StopTimeout` to `storeWait` and `defaultStopTimeout`.
`internal/launch`'s `TestStopNamesALauncherStillAdvancing` runs `Stop` over a real launcher whose advance is held.
