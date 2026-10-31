---
status: accepted
date: 2026-10-26
topic: review
amended-by: review-outcome-2.md
formerly: D47, 0047-review-skill.md, review-base-input.md, unconfigured-review-revisions.md
---

# The guildhall-review skill adapts upstream code-review to a stage that supplies its inputs

## Context

The release ships `guildhall-review` for automated `review` stages, adapted from upstream `code-review` as the [release skills root](release-skills-root.md) records.
Upstream wrote `code-review` for a session with a person, so what it asks a user for has to come from the stage instead.

## Decision

- **Two axes.**
  It keeps upstream's Spec and Standards axes, never merged or reranked: the spec line quoted for each spec finding, the standard cited for each standards finding, anything tooling enforces skipped, scope creep reported, and the smell baseline as labelled judgement calls in `smells.md`.
- **Inputs, not questions.**
  The head comes from the `head` input and the spec from the `spec` input by its recorded digest, which the renderer refuses to mismatch, and an empty range is a `rejected` outcome with a finding rather than a question.
- **The recorded base.**
  The review base reaches the prompt through the attempt's existing immutable binding: the [interpreter contract](../interpreter.md#rounds-and-loops) owns base selection and preparation, the [rendering contract](../rendering.md#artisan-prompts) the emitted field, and the [skill](../../skills/guildhall-review/SKILL.md#what-you-review) range selection and missing-base handling.
- **The standards.**
  They are the ones the prompt inlines, so the Standards axis measures against the exact words and revision the outcome binds to.
  With no standards revision configured, the outcome binds the engine's existing `""` ([review outcome](../configuration.md#review-outcome)), shown by the existing [bound-example rendering](../rendering.md#required-outputs-and-result-template).
- **The shared outcome.**
  The output is the shared review outcome, as [review outcome /2](review-outcome-2.md) defines it, rather than a new report format; the axis is the finding id's prefix, `spec-`, `std-` or `smell-`, because the outcome has one findings list and no axis field.
- **Later rounds.**
  Every earlier finding is dispositioned before new ones; a finding the builder disputed but did not fix stays in `findings`, so the Director sees the stall, and keeping a ruled finding open contests the ruling as [Director rulings](director-rulings.md) defines.
- **One reviewer.**
  Upstream's parallel sub-agents are dropped: a workflow that wants independent reviewers runs the skill in several attempts on distinct profiles, as the Guildhall repository's three reviews do.

## Alternatives rejected

- A default-branch fallback for the base: only the interim until the reviewer was given the recorded base.
- A workflow port or a second document holding the base commit: it duplicates what the binding already records.
- A sentinel such as `none` for an unconfigured standards revision: it would mean migrating bindings and interpreting a configured revision that happens to be named `none`.
- A second rendering path for an unconfigured standards revision.

## Consequences

A test checks the three-file artifact shape and passes the example outcome through `workflow.ParseReviewOutcome`, with no model call; that establishes the shape and example compatibility, not whether a model follows the instructions.
Nobody has yet run the two-axis format as an unattended Artisan outcome.
The [workflow](../contracts/workflow.md#tests) and [rendering](../contracts/render.md#tests) contract tests cover the unconfigured-standards case.

## History

- 2026-10-21, D47 (0047-review-skill.md): adopted the skill as above.
  On 2026-10-21 the captain approved ("Yes, yes") a default-branch fallback for the base as an interim, and contract-compatible adaptations to `guildhall.review-outcome/1`: rule-less suggestions in `findings`, `resolved`/`still-open`/`withdrawn` dispositions and the upstream one-line summary as the result file's `summary`.
  A separate suggestions list, other disposition states and an outcome summary were left as a follow-up, which [review outcome /2](review-outcome-2.md) delivered.
- 2026-10-26, review-base-input.md: replaced the interim fallback by exposing the recorded base through the existing binding, rather than a workflow port or a second document with the same commit, without changing the review outcome schemas.
- 2026-10-26, unconfigured-review-revisions.md: an omitted standards revision had been refused at intake, leaving the task waiting for the Director instead of advancing, and configured test fixtures had masked it.
  The engine's existing `""` was accepted rather than a `none` sentinel, and the existing bound-example rendering kept.
  `go test ./internal/interp -run 'Test(UnconfiguredStandardsReviewExample|BuiltinOutputInstructions)$' -count=1` failed before the validator change and passed after it.
