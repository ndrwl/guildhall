# Rails, not tracks

Status: accepted direction; this change records the decision and audit only.
Purpose text, Director tools and engine fixes follow in small changes after D3.

## Context

The default workflow is useful guidance, not a complete enumeration of everything that can happen during delivery.
D3 dogfood encountered four successive obstacles: a build result without a head output, a review without an outcome file, an outcome with an empty standards revision, and a published pull request conflicting with main while reporting no checks.
The Director needed engine changes to proceed rather than being able to resolve the situation with its tools.
Some of those specific failures are already addressed in the audited revision; the [escape-path audit](../evidence/rails-not-tracks-audit-2026-10-26.md) distinguishes those repairs from remaining gaps.

## Decision

Guildhall supplies **rails, not tracks**: the engine enforces delivery invariants, the workflow supplies the normal route, and the Director chooses a safe route when circumstances change.
The Director can be creative about how the work gets done, never about whether its obligations hold.
The engine remains deterministic and owns durable state and lifecycle; the Director reasons and orchestrates, and Artisans do the implementation work.
This extends R1.58 without introducing executable workflow code, user-defined step kinds, a generic workflow platform or another always-on service.

### Invariants, regardless of route

- **Accepted scope:** work remains bound to the specification the Guildmaster accepted, with its exact digest and answer preserved; a changed specification needs renewed acceptance before it authorizes implementation.
- **Independent final review:** the final code must receive a non-author review against that accepted specification and the applicable standards; changing the head invalidates stale evidence and requires review coverage of the new head, including the existing rebase-aware review rules where applicable, unless a recorded rebase proves coverage: tier-1 patch equivalence or a clean narrowed review of the resolved hunks ([merge queue and tiered rebase](merge-queue-rebase.md)).
- **Validation:** required checks and validation must pass for the delivered head; a missing check list is not success unless the repository explicitly declares no CI, and a route change cannot manufacture that declaration as a workaround.
- **Merge authority:** the Director cannot approve on the Guildmaster's behalf or merge without the authorization required by R1.36; a repository that requires `guildmaster_approval` needs the Guildmaster's recorded approval of the exact head, while a repository that turned it off in its own configuration is merged by Guildhall only when every other requirement holds, never by authority the Director can grant itself ([merge requirements](merge-requires.md)); an earlier merge approval carries over to a head Guildhall rebased only under the repository's `reapprove_after_rebase`, which the Director cannot grant either.
- **Full records and custody:** preserve inputs, outputs, attempts, errors, reviews, decisions, route changes and their reasons, including superseded evidence and unlanded work; retries and recovery must be identity-safe and replayable, never a deletion or rewrite of inconvenient history.

These are target engine guarantees, not a claim that today's implementation enforces every invariant for every admitted workflow.
In particular, current workflow approval can admit definitions below the structural review floor, and current delivery gates depend on the steps and inputs present in that definition.
Follow-up enforcement must make the invariants independent of route and admission exceptions; approval of a workflow is not a waiver of final independent review or required validation.
Existing Guildmaster decisions on disputed findings remain decisions on reviewed evidence, not permission for the Director to skip review.

### The Director chooses the route

The default ordered workflow and bounded loops remain the cheap, deterministic normal path.
When that path is unsuitable, the Director may send work back to any earlier step with instructions, re-run a step, add a tested built-in step, split the task, hold it, abandon it while preserving its work, or ask the Guildmaster.
Each move must state its purpose and evidence, retain task and attempt lineage, and pass the same invariant checks as normal advancement.
A split does not silently accept a new scope or authorize work on another Director; the existing task graph, scope acceptance and manual cross-Director handoff rules still apply.
A hold names what would allow progress and who can provide it; abandoning a task never marks it delivered.

A task keeps its immutable base workflow revision (R1.62).
The intended toolbox records per-task route changes alongside that revision rather than editing the pinned definition or overwriting previous attempts.
A route change identifies affected inputs and invalidated downstream evidence; reusing a prior review, validation or approval requires proof that its subject and other bindings still match.
Adding or moving a step cannot weaken review, checks or approval requirements; changes to those requirements or to the pinned workflow itself retain the existing Guildmaster approval and explicit migration boundary.
The exact route-record schema and commands are follow-up contracts, not commands implemented by this decision.
Until those tools exist, the Director must use the available safe operations or escalate, not edit the store, fabricate results, impersonate an approver or bypass a refusal.

### Purpose, not only procedure

Each workflow must explain the outcome it exists to achieve, and each step kind must explain its purpose, the evidence it produces and the invariant it serves.
The rendered Director role must explain the distinction between those obligations and the suggested sequence, and tell the Director how to discover lawful alternatives.
Purpose text should let the Director reason about a missing artifact, a conflict, a stalled worker or an unsuitable decomposition without treating an example sequence as the only permitted plan.
Adding that text to the registry, workflows and role prompt is deferred until after D3; this documentation change does not alter prompts or workflow digests.

### Never wedge silently

An unexpected state, failed advance or refusal must reach the Director with facts and available tools, even when the normal route has no continuation.
The response must identify the repository, task, step and attempt when they exist; the observed state and its age; relevant artifact, head, decision and error evidence; what the engine did or did not commit; and the safe next operations with their preconditions.
It must distinguish a normal human or external wait, a recoverable error, and an unsupported or ambiguous state.
A pending external wait must expose its owner, last observation and condition for resuming; lack of progress must become an actionable observation, not an endless repeated "pending" with no explanation.

Failure reporting is durable and deduplicated, and uses the existing inbox and wake path rather than spending model tokens on polling.
If the store or notification path itself is unavailable, the command must return an explicit failure and preserve local diagnostic evidence for later reporting, rather than claim that the Director was notified.
Unknown process ownership still forbids duplicate launch or unsafe termination; "never wedge silently" promises diagnosis and escalation, not an unsafe force switch or guaranteed autonomous recovery from every infrastructure failure.
The toolbox must always offer an honest hold or escalation when no lawful execution move is available.

### Maintenance tickets to the Steward

When an edge case requires intervention—an engine dead end, an unexpected state or a workaround—the Director files a maintenance ticket, including when it successfully resolves the immediate task.
This extends the designed messaging and ticket model, not the authority of a message.
The [maintenance-ticket reference](../messaging.md#maintenance-tickets-to-the-steward-designed-not-built) owns the evidence, authority, standalone operation, collection and triage contract; these capabilities are **not built**.

## Delivery plan and acceptance

1. This change: decision, aligned requirements and architecture, messaging extension and source-based audit; no engine, CLI or prompt behavior changes.
2. After D3: purpose text per workflow and step kind, plus the Director role's rails-and-tools section.
3. A small toolbox foundation: inspected state and legal actions, append-only route records and invariant checks, then individual send-back/re-run, add-step, split, hold/abandon and escalation slices.
4. Small recovery and observability fixes, prioritized by the audit, including maintenance capture and Steward collection as separate slices.

Each implementation slice follows [contract-first development](../contracts.md): specify identity, ownership, errors, idempotency and recovery at the real boundary, then test the observable behavior before implementing it.
Tests should exercise both normal and alternate routes, stale evidence after a head or scope change, duplicate requests, crash/replay, unknown ownership and failed notification, with real persistence/process coverage where relevant and zero model calls in CI.
Do not assert that purpose prose alone proves a Director will reason correctly; evaluate that separately during development and live acceptance.
