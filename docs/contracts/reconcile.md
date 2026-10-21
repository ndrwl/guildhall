# `internal/reconcile`

## Contract

`Classify(ctx, Attempt{Dir, Files, Owner}, Options{StaleAfter, Now})` classifies an owned attempt, files first and then the owner: `finished`, `cancelled` or `failed` from `outcome.json`, the cancel marker or a valid result; `absent` without an ownership record; then `exited`, `unknown`, `stuck` or `running` from the recorded launcher identity, its containment (`artisan.Contained`), its heartbeat and its endpoint ([reconcile](../artisan.md#reconcile)).
`stuck` is a live launcher identity with a heartbeat older than `StaleAfter` (`DefaultStaleAfter`); it is reported with its evidence and is never exit or relaunch authority.
Outcome, marker and result reads hold the same exclusive `attempt.lock` as launcher decisions, so an in-flight Cancel or Stop cannot publish its marker after classification accepts a result. Acquisition uses `artisan.LockAttempt` and is bounded by the caller's context and `LockTimeout`; an acquisition failure yields `unknown` with the lock error. The attempt directory must exist. The lock is released before owner and endpoint checks. The endpoint call is bounded by `EndpointTimeout`. Classify never signals, writes attempt state or takes over; acquiring the lock may create `attempt.lock`.

## Tests

`reconcile_test.go` was written red first against stub bodies of `Classify`, `Contained` and the launcher heartbeat; every case failed on its assertions ([red run](../evidence/reconcile-2026-10-21-red.txt)).
After the red commit two harness details changed, not expectations: the take-over request in the stale-heartbeat case had carried state `running`, which the store refuses as invalid before the ownership check.
Later, the wedged and stale-heartbeat cases were made to wait for the launcher's first heartbeat before blocking it: blocking earlier could stop the launcher before any beat, a race in the harness that never tested staleness.
New red: the four accepted heartbeat cases (a wedged launcher with a really blocked supervision loop, a stalled worker, a stale heartbeat while alive then recovery, a crashed launcher's frozen heartbeat).
Pinned from the scratch `Classify`: L13, L14 and L19 to L22.
The cases use live processes and real units, endpoints and stores; no model calls. The cancellation regressions added during review use an AttemptFiles double to pause real Launcher.Cancel and Launcher.Stop after their negative result check and before marker publication. A result published in that interval cannot be accepted: classification times out as `unknown` while the lock is held and returns `cancelled` after release. A separate contention case exercises the default lock timeout.
