---
status: accepted
date: 2026-10-22
topic: workflow
formerly: D22, 0022-workflow-approvals-per-director-keyed-by.md, D56, 0056-approval-enforcement-at-task-creation.md
---

# Workflow approvals are per-Director store records keyed by digest, and task creation admits only an authorized workflow

## Context

The Guildmaster must approve a workflow change that affects review requirements, required checks or approval steps ([workflow-edit authority](../configuration.md#workflow-edit-authority)), and no Director may approve its own edit.
An approval had to cover exactly the content the Guildmaster saw, and `task new` had to refuse a workflow nobody authorized.

## Decision

An approval is a store record keyed by the definition's digest, not its name, so an edit always needs a new approval and an approval never covers content it did not see.
Approvals belong to the Director, not a repository or task, so they live in a top-level `workflow-approvals` bucket created on first use; the store stays at schema 1.
The mutation id is derived from the digest, so the store's replay and conflict rules make an approval idempotent and refuse different words for the same revision without a second check.
Definitions shipped with a release count as approved by that release without a stored record, so a release that changes them needs no migration.

`task new` admits a repository's effective workflow only when its digest is shipped with the release, approved by the Guildmaster in the Director's store, or a change that meets the review floor and weakens nothing in an authorized baseline ([enforcement at task creation](../configuration.md#enforcement-at-task-creation)).
The policy is `workflow.Authority.Admit`, a pure function over definitions: the store supplies approved digests and the CLI supplies the shipped definitions and the repository's file, so the policy is tested without a store.
The baselines are the shipped definitions and the repository's workflow file before its overrides, the only authorized definitions whose content Guildhall holds, since an approval records a digest, not a definition.
The classifier follows the Steward's managed-setting classification ([rollout approval](../steward.md#approval)): a fixed list of changes needs approval, and anything it cannot prove harmless, including any reorder of shared steps and any setting change on a guarded step, counts as weakening.
The review floor, one non-author review and one validation before delivery, is checked at admission, not in `workflow.Validate`, so a shipped or approved workflow below it, such as a Director-local test workflow, stays valid, and the Guildmaster's approval of the exact digest is what lowers it.
`workflow approve --config --repo` approves a repository's effective workflow, resolved exactly as `task new` resolves it, because an overridden definition exists in no file.

## Alternatives rejected

- Approvals keyed by workflow name: an edit would inherit an approval for content the Guildmaster never saw.
- Approvals per repository or task: the Guildmaster's authority is per Director.
- Storing approved definitions so any of them can be a baseline: left out to keep the store unchanged, so a non-weakening change from some other approved revision needs its own approval.
- Reasoning about which reorders or setting changes are safe: a conservative fixed list is easier to trust.

## Consequences

The classifier sees definitions only, so a changed role or skill file that a profile or skill name points to is invisible to it; binding those files to a task is pinning work, not this decision.
The design called for the review floor to come from a managed policy key; the floor is fixed until that key is designed.
Role profiles are resolved outside the digest, so changing a profile changes who runs a step without an approval ([role profiles outside the digest](role-profiles-outside-the-digest.md)).

## History

- 2026-10-20, D22 (0022-workflow-approvals-per-director-keyed-by.md): approvals per Director, keyed by digest, in a top-level bucket, idempotent by a digest-derived mutation id, with shipped definitions approved by the release; unchanged since.
- 2026-10-22, D56 (0056-approval-enforcement-at-task-creation.md): enforced approval at `task new` through the pure `workflow.Authority.Admit`, with the shipped and pre-override file baselines, the conservative classifier, the fixed review floor outside `Validate`, and `workflow approve --config --repo`; it rejected storing approved definitions as baselines; unchanged since, though [task kinds](task-kinds.md) later added `--kind`.
