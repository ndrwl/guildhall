---
status: accepted
date: 2026-10-29
topic: workflow
tasks: d9, d43, d74
formerly: D95, 0095-bug-workflow-without-acceptance.md, research-build-instructions.md, research-profile.md
---

# A task is created as a feature, bug or hallmark kind, and the shipped bug workflow runs with no grill and no acceptance

## Context

Every task ran the repository's one `workflow` setting, so a small defect went through the same grill and Guildmaster acceptance as new behaviour (d9).
The Guildmaster asked for a bug task to run without a Guildmaster stage before merge (d43), and later for a report-only hallmark task (d74).

## Decision

`task new --kind` creates a task as a **task kind**, `feature`, `bug` or `hallmark`; `feature` is the default and `--fixes` without `--kind` means `bug` ([task kinds](../tasks.md#task-kinds)).
A `feature` runs the repository's `workflow` setting, the shipped `default` unchanged byte for byte.
A `bug` runs the shipped `bug` workflow, or the one `[repository.kinds] bug` names; a `hallmark` runs the shipped report-only `hallmark` workflow ([report-only workflows](report-only-workflows.md)), or `[repository.kinds] hallmark`.
`[repository.kinds]` accepts only `bug` and `hallmark`, and is omitted from JSON when unset so existing config digests do not change.
The task records no kind, since its bound workflow's name says what it was created as; the kind descriptions live in Go (`task new --help`, copied into the Director's tasks skill), so rewording them changes no digest; Go calls it `taskKind`, never a bare `Kind`, which is the step kind.
`config.TaskKindTable` is the one table of the kinds, their descriptions and default workflows, and `interp.TaskKindOf` is the one reading of a task's kind: `hallmark` when its bound workflow resolves and has a hallmark step, then `bug` when it is bound to the repository's bug workflow name and that is not its `workflow`, else `feature`, so when `kinds.bug` equals `workflow` its tasks read `feature`.
`workflow approve --config --repo --kind` approves a repository's own overrides of a kind's workflow ([workflow approvals](workflow-approvals.md)).

The shipped `bug` workflow drops `grill` and `accept` and runs research, plan, build, review, publish, validate and deliver; the shipped `default` and a copy's own workflows keep both.
Build, review and rebase take plan's spec: their `spec` input is typed `accepted-spec` and lists `document` in `Port.Also`, and no other port lists extra types.
The spec gate follows the source's output type, not the port's, so a `plan.spec` passes ungated, while review outcomes, the rebaser and deliver's merge authorization still bind to the digest of the spec the build was given ([spec gate](../interpreter.md#spec-gate)).
`guildmaster-approval` stays guarded, so a custom workflow that drops `accept` from an authorized baseline is still a weakening.
Every Artisan prompt shows the task title after its header, fenced as text, because with no grill nothing else states the defect; the title is not in the manifest's `Identity`.

`research` accepts `profile` and `instructions` but not `skills`; `plan` and `build` accept `instructions`, a registry change only, since the renderer already renders any step's `instructions` and none of these kinds is guarded ([built-in step kinds](../configuration.md#built-in-step-kinds)).
The bug workflow's research reproduces the defect and finds its cause, or fails with what it tried and found; its plan takes the recommended answer to an ordinary open question and records it, or fails with a question for the Guildmaster; its build writes a regression test first, shows it failing before the fix and passing after, and names it in the commit.
A failed attempt's summary carries the finding or question, which the Director reads with `task show` and brings to the Guildmaster before any `step retry`; the `attempt-failed` message is unchanged.

## Alternatives rejected

- A second shipped definition named `feature`: the same content under another digest.
- Recording the kind on the task: it would change the store and its goldens for nothing the workflow name does not say.
- A new engine-run acceptance step kind, or an engine actor on `guildmaster-approval`: either records a self-approval the [workflow-edit authority](../configuration.md#workflow-edit-authority) rules out.
- `guildhall-contract-tdd` for the bug build: it is contract TDD against a spec and its checks run only on Go, while "regression test first" works in any language.
- Research-specific validation for its profile: the registry's existing Artisan `profile` definition is reused.

## Consequences

Where a repository's [merge requirements](../configuration.md#merge-requirements) leave `guildmaster_approval` off, as guildhall's do, a bug fix is planned, built, independently reviewed, validated and merged without any human reading its spec; the review floor still holds.
The type check no longer proves that a build's spec was accepted; the source and the workflow-edit authority do.
The task title now appears in the default workflow's prompts too.

## History

- 2026-10-23, research-profile.md: research steps accepted an optional `profile` only, still refusing `skills`, reusing the registry's Artisan profile definition rather than research-specific validation or launch; it narrowed an original broader shared-settings scope and fixed task creation refusing a research profile, with red and green evidence linked from the [workflow](../contracts/workflow.md#tests) and [interpreter](../contracts/interp.md#tests) contracts.
- 2026-10-29, task-kinds.md (d9): introduced the `feature` and `bug` kinds, with `--kind` on `workflow approve` because the shipped `bug` with a defaulted reviewer profile then needed approval; once [role profiles left the digest](role-profiles-outside-the-digest.md) the shipped `bug` needed none, so `--kind` approves only a repository's own overrides. d74 added `hallmark`.
- 2026-10-31, task-kinds.md (d95, hallmark d90 H13 and H7): the kinds moved into one table, `config.TaskKindTable`, and the kind reading into `interp.TaskKindOf`, which finds a hallmark task by a hallmark step in its bound workflow rather than by name, so a hallmark task still reads `hallmark` after its repository renames `kinds.hallmark`; a task bound to a hallmark workflow that no longer resolves now reads `feature`.
- 2026-10-29, research-build-instructions.md (d9, d43): `research` and `build` accepted `instructions`, then `plan` too for the bug workflow, with the bug's research, plan and build instructions; it rejected `guildhall-contract-tdd` for the bug build.
- 2026-10-29, D95 (0095-bug-workflow-without-acceptance.md, d43): the bug workflow without grill and acceptance, `Port.Also`, the source-following spec gate, the task title in prompts and failed attempts carrying questions; it rejected a new acceptance step kind.
  Its rollout told copies whose role profiles changed the bug review step to re-approve with `--kind bug`, which role profiles leaving the digest made unnecessary; an in-flight bug task bound to the old digest would have stranded until [retired shipped revisions](retired-shipped-revisions.md) kept it resolvable.
