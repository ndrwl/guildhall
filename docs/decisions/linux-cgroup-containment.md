---
status: accepted
date: 2026-10-21
topic: containment
amends: artisan-containment.md
formerly: D33, 0033-linux-cgroup-containment.md
---

# Linux containment uses a delegated cgroup v2, else a subreaper and process group

## Context

The [containment interface](artisan-containment.md) first shipped Linux with a process group alone, as an interim backend.
A process group cannot hold a descendant that calls `setsid`, and an orphaned descendant is reparented away from the launcher.

## Decision

A Linux unit is a child cgroup `guildhall-<name>` of the launcher's own cgroup when `/sys/fs/cgroup` is cgroup v2, this user may write that cgroup and its `cgroup.procs`, and it has `cgroup.kill`.
Otherwise it is a process group with the launcher as child subreaper ([Artisan launcher](../artisan.md#linux-detection)).
The worker is created inside the cgroup with `CLONE_INTO_CGROUP`.
No controller is enabled, so a delegated cgroup that already holds the launcher can hold the child and `cgroup.subtree_control` is not required.
Detection reads the host's own files, so the reason it records says which check failed; a cgroup that detection allowed but that cannot be created is an error, not a silent fallback.

## Alternatives rejected

- Moving the worker into the cgroup after it starts: a descendant could be created outside it.
- Trying to create a cgroup first to detect support: the recorded reason would not say which check failed.
- Enabling controllers: it would need `cgroup.subtree_control` and could not use a delegated cgroup that already holds the launcher.
- Falling back silently when an allowed cgroup cannot be created: it would hide a broken host behind weaker containment.

## Consequences

Under the fallback a `setsid` escape stays reported, not contained: the subreaper makes an orphaned escape a child of the launcher, where `Escapes` reports it, but `Kill` does not reach it.
The `RecordContainment` helper provides the [attempt-directory record](../artisan.md#recording-the-containment) for later launcher integration; creating a unit does not persist it automatically.
How the store keeps the unit is [containment persistence](containment-persistence.md).
