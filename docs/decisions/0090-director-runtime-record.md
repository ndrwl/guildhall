# D90. Recording the Director runtime's containment and leaving every held start's wait

Sustainability review 12 found that a Director runtime could not be found again once its supervisor died (F42).
The session record named the supervisor and nothing else, so after a `kill -9` of `director start` the runtime ran on, `director stop` recorded the session `superseded` and ended nothing, and the next `director start` ran a second Director beside the first on one copy.
It also found that a `task start` or `step retry` the Director's hold refused before the advance recorded nothing, so a task asked for during a pause was missing from `needs_me` after the resume (F47).

## Decisions

- **The session records its runtime's unit.**
  Once `artisan.StartRecorded` creates the gated or suspended runtime process, the supervisor records the unit on the session with `store.RecordDirectorRuntime`, the same `store.Containment` an Artisan's ownership records for its worker: kind, name, reason and the runtime's identity.
  The record is its own mutation keyed by the session, `director-runtime:<n>`, rather than part of the start transaction, because the unit's description and the runtime's identity exist only once the runtime has started, after the session is recorded and rendered.
  It does not move the session's state sequence, so it never races the runtime's own `director ready`.
  A failed record ends the runtime and its unit, as a failed ownership record kills an Artisan's worker.
- **A gone supervisor's runtime is swept by `director stop` with the owner-gone rule.**
  The session's supervisor is the unit's owner while it lives; once `artisan.Probe` proves it gone, `director stop` is.
  It sweeps the recorded unit as `artisan resume` sweeps a gone launcher's (`launch.SweepOwner`, the resume sweep's loop over `reconcile.ProbeOwner`), within the resume sweep's 10 seconds, before recording the session `superseded`.
  A survivor it cannot prove ended, or a unit it cannot read, is reported on standard error and makes the exit 1, leaving the session `stopping` and `sweeping`, so the hold stays; the diagnostic tells the user to run `guildhall director stop` again.
- **`director start` refuses while the recorded unit holds a process.**
  `checkSupersede` reads the newest session's recorded unit with `artisan.Contained` in the transaction that records the next session, and refuses with `director: live`, naming the survivors and `director stop`, unless it is proven empty.
  This holds whatever the session's state, since a session is recorded `stopped` by its own supervisor only after the unit is empty and as `superseded` only after the sweep.
- **The program cannot run before its record commits.**
  On Unix a shell in the runtime's unit waits on a private pipe before executing the runtime; the supervisor releases it only after recording its identity and containment.
  A supervisor crash before release closes the pipe, so the shell exits without executing the program.
  After release the recorded unit supports recovery.
  On Windows the process remains suspended in its kill-on-close Job Object until the record commits; supervisor death closes the job.
  Unproven cleanup and previously stopped sessions follow the [stop recovery rules](../director.md#stop).
- **A held start leaves its wait.**
  `task start` and `step retry` no longer read the hold before the advance; the engine's `Hold`, and the start transaction's `AttemptCaps.Hold` behind it, are the one check, and a held advance writes `{"wait":"paused","step":"<step>"}` to the task's `next.json`, which `needs_me` lists as `paused`.
  The pre-advance read only spared an advance that would be held its preparation, at the cost of the record the resume relies on.
