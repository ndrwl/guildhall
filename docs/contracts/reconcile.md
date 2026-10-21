# `internal/reconcile`

## Contract

`Classify(ctx, Attempt{Dir, Files, Owner}, Options{StaleAfter, Now})` classifies an owned attempt, files first and then the owner: `finished`, `cancelled` or `failed` from `outcome.json` (`artisan.ReadOutcome`; unreadable is `unknown`, a newer schema reported apart from damage), the cancel marker or a valid result; `absent` without an ownership record; then `exited`, `unknown`, `stuck` or `running` from the recorded launcher identity, its containment (`artisan.Contained`), its heartbeat and its endpoint ([reconcile](../artisan.md#reconcile)).
`stuck` is a live launcher identity with a heartbeat older than `StaleAfter` (`DefaultStaleAfter`); it is reported with its evidence and is never exit or relaunch authority.
The [package API contract](../../internal/reconcile/reconcile.go) defines attempt-lock synchronization, bounded lock and endpoint waits, and the lock-file creation exception to otherwise non-mutating classification.
The attempt directory must exist.
`ProbeOwner` is the one owner-gone rule: it probes the recorded launcher, translates the record's containment and lists its survivors with `artisan.Contained`; `Owner.Gone` holds only for a gone launcher whose recorded unit is proven empty.
`Classify`, `launch.ProbeOwner` and `launch.Resume` read it.

## Tests

`reconcile_test.go` was written red first against stub bodies of `Classify`, `Contained` and the launcher heartbeat; every case failed on its assertions ([red run](../evidence/reconcile-2026-10-21-red.txt)).
After the red commit two harness details changed, not expectations: the take-over request in the stale-heartbeat case had carried state `running`, which the store refuses as invalid before the ownership check.
Later, the wedged and stale-heartbeat cases were made to wait for the launcher's first heartbeat before blocking it: blocking earlier could stop the launcher before any beat, a race in the harness that never tested staleness.
New red: the four accepted heartbeat cases (a wedged launcher with a really blocked supervision loop, a stalled worker, a stale heartbeat while alive then recovery, a crashed launcher's frozen heartbeat).
Pinned from the scratch `Classify`: L13, L14 and L19 to L22.
The newer, damaged and unknown-state outcome cases in `TestClassifyFiles` were red against the lenient outcome read: a damaged or unknown-state file decided the attempt, and a newer one was reported as unreadable.
The cases use live processes and real units, endpoints and stores; no model calls.
The cancellation regressions added during review use an AttemptFiles double to pause real Launcher.Cancel and Launcher.Stop after their negative result check and before marker publication.
A result published in that interval cannot be accepted: classification times out as `unknown` while the lock is held and returns `cancelled` after release.
A separate contention case exercises the default lock timeout.
`ProbeOwner` was extracted from `Classify` and `launch.ProbeOwner` without changing either (sustainability review 7, F18); the existing L13, L14, take-over and stop-proof cases pin it.
`owner_test.go` (`TestProbeOwner`) tests the rule directly, written after the extraction rather than red first: a live launcher is alive without its unit checked; a gone launcher is not gone without recorded containment, with survivors (named) or with a unit that cannot be checked; it is gone once its translated unit is proven empty.
