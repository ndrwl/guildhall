---
status: accepted
date: 2026-10-26
topic: workflow
---

# Rails, not tracks: the engine enforces delivery invariants, the workflow supplies the normal route, and the Director chooses a safe route when circumstances change

## Context

The default workflow is useful guidance, not a complete enumeration of everything that can happen during delivery.
A dogfood delivery run encountered four successive obstacles: a build result without a head output, a review without an outcome file, an outcome with an empty standards revision, and a published pull request conflicting with main while reporting no checks.
The Director needed engine changes to proceed rather than being able to resolve the situation with its tools.
Some of those failures were already addressed in the audited revision; the [escape-path audit](../evidence/rails-not-tracks-audit-2026-10-26.md) distinguishes those repairs from remaining gaps.

## Decision

Guildhall supplies **rails, not tracks**: the Director can be creative about how the work gets done, never about whether its obligations hold.
The engine remains deterministic and owns durable state and lifecycle; the Director reasons and orchestrates, and Artisans do the implementation work.
This extends R1.58 without introducing executable workflow code, user-defined step kinds, a generic workflow platform or another always-on service.

### Invariants, regardless of route

- **Accepted scope:** work remains bound to the specification the Guildmaster accepted, with its exact digest and answer preserved; a changed specification needs renewed acceptance before it authorizes implementation.
- **Independent final review:** the final code must receive a non-author review against that accepted specification and the applicable standards; changing the head invalidates stale evidence and requires review coverage of the new head, unless a recorded rebase proves coverage: tier-1 patch equivalence or a clean narrowed review of the resolved hunks ([merge queue and tiered rebase](merge-queue-rebase.md)).
- **Validation:** required checks and validation must pass for the delivered head; a missing check list is not success unless the repository explicitly declares no CI, and a route change cannot manufacture that declaration as a workaround.
- **Merge authority:** the Director cannot approve on the Guildmaster's behalf or merge without the authorization required by R1.36; a repository that requires `guildmaster_approval` needs the Guildmaster's recorded approval of the exact head, while a repository that turned it off is merged by Guildhall only when every other requirement holds, never by authority the Director can grant itself ([merge requirements](delivery-stage.md)); an earlier approval carries over to a head Guildhall rebased only under the repository's `reapprove_after_rebase`, which the Director cannot grant either.
- **Full records and custody:** preserve inputs, outputs, attempts, errors, reviews, decisions, route changes and their reasons, including superseded evidence and unlanded work; retries and recovery must be identity-safe and replayable, never a deletion or rewrite of inconvenient history.

### The Director chooses the route

The default ordered workflow and bounded loops remain the cheap, deterministic normal path.
When it is unsuitable, the Director may send work back to any earlier step with instructions, re-run a step, add a tested built-in step, split the task, hold it, abandon it while preserving its work, or ask the Guildmaster.
Each move states its purpose and evidence, retains task and attempt lineage, and passes the same invariant checks as normal advancement.
A split does not silently accept a new scope or authorize work on another Director; a hold names what would allow progress and who can provide it; abandoning a task never marks it delivered.
A task keeps its immutable base workflow revision (R1.62); route changes are recorded per task alongside it, identify invalidated downstream evidence, and cannot weaken review, checks or approval requirements.
Until those tools exist, the Director must use the available safe operations or escalate, not edit the store, fabricate results, impersonate an approver or bypass a refusal.

### Purpose, not only procedure, and never wedge silently

Each workflow explains the outcome it exists to achieve, and each step kind its purpose, the evidence it produces and the invariant it serves, so the Director can reason about a missing artifact, a conflict, a stalled worker or an unsuitable decomposition.
The rendered Director role explains the difference between those obligations and the suggested sequence, and how to discover lawful alternatives.
An unexpected state, failed advance or refusal must reach the Director with facts and available tools: the repository, task, step and attempt; the observed state and its age; relevant evidence; what the engine did or did not commit; and the safe next operations with their preconditions.
It distinguishes a normal human or external wait, a recoverable error, and an unsupported or ambiguous state; lack of progress becomes an actionable observation, not an endless "pending".
Failure reporting is durable and deduplicated and uses the existing inbox and wake path rather than spending model tokens on polling; if that path is unavailable, the command returns an explicit failure and preserves local diagnostic evidence.
When an edge case needs intervention, the Director files a maintenance ticket to the Steward, even when it resolves the immediate task ([maintenance tickets](../messaging.md#maintenance-tickets-to-the-steward-designed-not-built), not built).

## Consequences

These are target engine guarantees, not a claim that today's implementation enforces every invariant for every admitted workflow.
Current workflow approval can admit definitions below the structural review floor, and current delivery gates depend on the steps and inputs present in that definition; follow-up enforcement must make the invariants independent of route and admission exceptions, and approval of a workflow is not a waiver of final independent review or required validation.
Existing Guildmaster decisions on disputed findings remain decisions on reviewed evidence, not permission for the Director to skip review.
Unknown process ownership still forbids duplicate launch or unsafe termination: "never wedge silently" promises diagnosis and escalation, not an unsafe force switch or guaranteed autonomous recovery, and the toolbox always offers an honest hold or escalation.
The route-record schema, the Director's toolbox commands and the purpose text in the registry, workflows and role prompt are follow-up work, each specified contract first.
