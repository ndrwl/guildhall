---
status: accepted
date: 2026-10-26
topic: workflow
---

# An opt-in refute step attacks the plan before acceptance, as advisory findings

## Context

A plan reaches the Guildmaster's acceptance with no independent attack on its assumptions, edge cases, alternatives and risks.

## Decision

Add `refute` version 1 as an Artisan-only document producer with required `spec`, optional many-document research `context`, and a `findings` output.
The shared Artisan role plus a dedicated baseline stage guide request an independent attack, with a severity and a suggested spec change for every finding.
The output is advisory Markdown, not a review outcome: it neither approves the spec nor gates delivery as a code or document review does.
Guildmaster acceptance gains optional many-document `findings`, so the acceptance question lists the spec and the refutation together while the spec digest stays its subject.
The existing generated output instructions advertise `findings.md` without a special renderer branch.
It ships as an opt-in workflow example using configured profiles ([configuration](../configuration.md#opt-in-plan-refutation), [interpreter](../interpreter.md#plan-refutation)).

## Alternatives rejected

- Using `document-review`: it would conflate adversarial advice with the typed review verdict and review-gate contracts.
- An automatic rewrite stage or a refute-specific loop: the existing acceptance loop back to plan already re-runs refutation under bounded rules.
- New interfaces: the existing typed registry, renderer, launch seam and document intake suffice.
- Changing the shipped default workflow now: it waits for a real-task trial.

## Consequences

The [workflow contract](../contracts/workflow.md#refutation-contract) preceded the new tests and registry implementation, and the [interpreter contract](../contracts/interp.md#tests) links the red and green focused runs, with both record backends and the fake runtime, without model calls.
Prompt and findings exchange is covered deterministically; whether the critique is useful, and a real-task trial, remain future evaluation, not a claimed result.
