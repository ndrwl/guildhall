# Heartbeat failure check — 2026-10-21

This check was performed after the heartbeat implementation was written, following the operator's R5 decision.
It does not establish historical pre-implementation red-first execution or authorize a process exception.

## Temporary stub

Only the bodies of `store.Store.Heartbeat` and `interptest.Records.Heartbeat` were temporarily replaced with returns of their zero heartbeat value and `store.ErrNotFound` (unqualified `ErrNotFound` within store).
Heartbeat writes, engine notification logic, and tests were unchanged.
Both read implementations were restored exactly after the failing run; no stub is retained.

## Command and results

Both runs execute the same focused command, with the test cache disabled:

```sh
go test ./internal/interp -run '^(TestWorkerStalled|TestRecordsContract)$/^(store|double)$/^heartbeat$' -count=1 -v
```

- [Stubbed run output](heartbeat-2026-10-21-red.txt): exit 1.
  `TestRecordsContract/store/heartbeat` and `TestRecordsContract/double/heartbeat` failed because a heartbeat written successfully could not be read (`record not found`).
  `TestWorkerStalled/double` and `TestWorkerStalled/store` failed at the first fresh-heartbeat stall crossing (`stalls = 0, want 1`).
  These were behavioral assertion failures, not compilation failures.
- [Restored implementation output](heartbeat-2026-10-21-green.txt): the focused verification after removing both stubs.

The contract tests exercise persistence and replay through both backends; the worker tests exercise observable Director messages and unchanged attempts through `Engine.Advance`.
No model calls or credentials are used.
