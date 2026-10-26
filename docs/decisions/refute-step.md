# Opt-in plan refutation before acceptance

## Decision

Add `refute` version 1 as an Artisan-only document producer with required `spec`, optional many-document research `context`, and `findings` output.
Use the shared Artisan role plus a dedicated baseline stage guide to request an independent attack on assumptions, edge cases, alternatives and risks, with severity and a suggested spec change for every finding.
Keep the output as advisory Markdown rather than a review outcome: it neither approves the spec nor gates delivery as a code or document review does.
Extend Guildmaster acceptance with optional many-document `findings` so the acceptance question lists the spec and refutation together while retaining the spec digest as its subject.
Existing generated output instructions advertise `findings.md` without a special renderer branch.

## Scope and alternatives

Ship an opt-in workflow example using configured profiles; do not change the default before a real-task trial.
The existing acceptance loop back to plan already re-runs subsequent refutation with bounded rules, so no automatic rewrite stage or additional loop protocol is needed.
Using `document-review` instead would conflate adversarial advice with the typed review verdict and review-gate contracts.
No speculative interfaces are added: the existing typed registry, renderer, launch seam and document intake suffice.

## Evidence

The [workflow contract](../contracts/workflow.md#refutation-contract) preceded the new tests and registry implementation.
The [interpreter contract](../contracts/interp.md#tests) links the captured red and green focused runs, with both record backends and the fake runtime, without model calls.
Prompt and findings exchange is covered deterministically; critique usefulness and a real-task trial remain future evaluation, not a claimed result.
