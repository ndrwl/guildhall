---
status: accepted
date: 2026-10-23
topic: director
formerly: D90, 0090-director-runtime-record.md
---

# The Director's runtime runs in a containment unit recorded on its session before its program can run

## Context

A stop that signalled only the runtime's own process let a shell or tool the runtime had started outlive it.
Sustainability review 12 then found that a runtime could not be found again once its supervisor died (F42): after a `kill -9` of `director start` the runtime ran on, `director stop` recorded the session `superseded` and ended nothing, and the next `director start` ran a second Director beside the first on one copy.
It also found that a `task start` or `step retry` refused by the hold before its advance recorded nothing, so a task asked for during a pause was missing from `needs_me` after the resume (F47).

## Decision

- The supervisor starts the runtime in `artisan.NewSessionUnit`, the Artisan's containment backend, and ends the whole unit rather than one process; however the runtime ends, it kills what is left and returns only once the unit is empty, so a replacement session never overlaps the old one's tools ([start](../director.md#start)).
  The launchers of the attempts the runtime starts leave the unit where the backend allows, because they keep their own ownership and stop.
- The session records its runtime's unit with `store.RecordDirectorRuntime`, the same `store.Containment` an Artisan's ownership records: kind, name, reason and the runtime's identity ([the runtime's record](../director.md#the-runtimes-record)).
  It is its own mutation, `director-runtime:<n>`, because the unit and identity exist only once the runtime has started, after the session is recorded and rendered; it does not move the state sequence, so it never races `director ready`, and a failed record ends the runtime and its unit.
- The program cannot run before its record commits: on Unix a shell in the unit waits on a private pipe the supervisor releases after recording, and on Windows the process stays suspended in its kill-on-close Job Object.
- Once `artisan.Probe` proves a session's supervisor gone, `director stop` owns its unit and sweeps it with the owner-gone rule (`launch.SweepOwner`, within the resume sweep's 10 seconds) before recording the session `superseded`; a survivor or an unreadable unit exits 1 and leaves the session `stopping` and sweeping, so the hold stays ([stop](../director.md#stop)).
- `director start` refuses with `director: live` while the newest session's recorded unit holds a process, whatever the session's state, read with `artisan.Contained` in the transaction that records the next session.
- `task start` and `step retry` no longer read the hold before the advance; the engine's `Hold` and the start transaction behind it are the one check, so a held advance writes its `paused` wait ([pause and stop](director-pause-stop.md)).

## Alternatives rejected

- Recording the unit in the start transaction: the unit's description and the runtime's identity do not exist yet.
- Keeping the pre-advance hold read: it only spared a held advance its preparation, at the cost of the record the resume relies on.

## Consequences

The unit's limits are the Artisan's: on a process-group backend a descendant that calls `setsid` leaves it, and on the subreaper backend an orphaned launcher stays a zombie until the supervisor exits.
On Windows the unit keeps the runtime in the supervisor's console process group so Ctrl+C reaches it, and its interrupt rung is refused.
Where a launcher cannot leave the unit, it starts in it and ends with the session.

## History

- 2026-10-22, D84 (0084-director-stop-admission.md), its containment part: put the runtime in a containment unit after the cross-review, with launchers leaving it through `GUILDHALL_DETACH_CGROUP` in a cgroup unit or job breakaway on Windows, and Linux's parent-death signal ending the runtime when its supervisor dies; the rest of that record is in [pause and stop](director-pause-stop.md).
- 2026-10-23, D90 (0090-director-runtime-record.md): recorded the unit on the session so a gone supervisor's runtime can be swept and a second Director refused, gated the program until the record commits, and made a held start leave its `paused` wait (F42, F47).
  A byte-identical copy, `director-runtime-record.md`, was re-added by #128 after #130's numbering and was deleted in favour of this record.
