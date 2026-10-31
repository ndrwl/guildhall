---
status: accepted
date: 2026-10-21
topic: containment
formerly: D48, 0048-artisan-resume.md
---

# Artisan resume sweeps a recorded unit only after the owner-gone rule

## Context

An attempt whose launcher has exited needs a reconcile entry that can clean up its contained processes and settle it.
The rule for "the owner is gone" already existed twice, in `launch.ProbeOwner` and `reconcile.Classify` (sustainability review 7, F18).

## Decision

`guildhall artisan resume` is the reconcile entry for an attempt whose launcher has exited ([resume](../artisan.md#resume)).
It sweeps the recorded unit only when the recorded launcher identity is proven gone, and settles the attempt only once `reconcile.ProbeOwner`, the one owner-gone rule, proves the unit empty.
A live or unproven owner, or a record without containment, is refused and nothing is signalled.
`artisan.Sweep` kills by recorded [containment](containment-persistence.md) from outside the launcher: a Windows job by name, a Linux cgroup through `cgroup.kill`, and a process group member by member with `artisan.Kill`, which signals only a process whose PID and token still match.
The decision is written with `artisan.Settle` under the attempt lock, with the launcher's [precedence](stop-ladder-precedence.md) and an existing `outcome.json` kept.
The store outcome is recorded in the same lock while the record still names the gone launcher, so a concurrent take-over or resume cannot be finished by mistake.
Resume adds no store record kind: it writes `FinishAttempt` with a mutation id derived from the attempt and the gone launcher, so a rerun records the same outcome.

## Alternatives rejected

- Writing the owner-gone rule a third time for resume: it was extracted from the two existing copies instead.

## Consequences

Escapes from a process group are not swept; that limit is the containment's, already reported.
The interpreter's replay matrix covers only the engine's writes and is unchanged.
`launch.ResumeArgv` starts with the plain command name `guildhall`, because Herdr refuses a path as `resume_argv`; production registration is described under [workflow Artisan workspaces](../artisan.md#workflow-artisan-workspaces).
The lab harness commands sit under `guildhall artisan harness` and refuse to run without `--lab`, because they are test tooling.
