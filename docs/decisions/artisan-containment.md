---
status: accepted
date: 2026-10-21
topic: containment
amended-by: linux-cgroup-containment.md
formerly: D28, 0028-artisan-containment.md
---

# The launcher owns each worker through one containment interface with a backend per platform

## Context

The launcher must stop an Artisan worker and everything it started on Windows, Linux and macOS.
A delivered signal or a closed terminal does not prove that processes ended, and some platforms cannot hold every descendant.

## Decision

The launcher owns each worker through one containment interface with a backend per platform ([Artisan launcher](../artisan.md#containment-unit)):

- Windows: a Job Object that kills on close and refuses breakaway.
- Linux: cgroup v2 when delegated, otherwise subreaper plus process group, as [Linux containment](linux-cgroup-containment.md) decides.
- macOS: a process group.

The process-group leader stays unreaped so its id cannot be reused.
A stop counts only when the unit is observed empty.
Where a platform cannot contain a descendant, such as a `setsid` escape from a Unix process group, the limit is reported and tested as an escape rather than claimed as contained.
One contract suite runs against every backend on its native runner and against the double the launcher's tests use.

## Consequences

A process-group backend leaves escapes uncontained; they are visible, not prevented.
Every backend must pass the same contract suite, so a new platform is a new backend, not a special case in the launcher.
When this was accepted the Linux backend was the process group alone, as an interim; [Linux containment](linux-cgroup-containment.md) replaced that interim with a delegated cgroup v2 and the subreaper fallback.
