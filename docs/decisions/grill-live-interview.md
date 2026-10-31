---
status: accepted
date: 2026-10-22
topic: director
formerly: D29, 0029-grill-step-skills.md, D61, 0061-grill-live-interview.md
---

# The grill is the Director's live interview of the Guildmaster, recorded as the stage's notes

## Context

A grill stage exists so the Artisans build the product the Guildmaster wants.
The first shipped grill skill, `guildhall-grill-notes`, told the stage that nobody answers it and to proceed on its own recommendations, which inverted that purpose.

## Decision

With the default `director` actor the grill is a live interview in the Director's conversation with the Guildmaster: rounds of numbered questions over a design tree, each with a recommended answer worded so "yes" accepts it, until both share an understanding.
The agreed result, every settled decision with the Guildmaster's answer, is recorded as the stage's notes, so it is stored durably and no later stage depends on the conversation (R1.13).
Its release skills are `guildhall-grill`, adapted from `grilling` in mattpocock/skills, and `guildhall-domain-model`, adapted from `domain-modeling`, the pair `grill-with-docs` invokes upstream; the shipped default grill lists both and the engine hands the stage to the Director ([Director stages](engine-advance.md)).
`guildhall-grill-notes` is renamed `guildhall-grill-unattended`, kept only as the opt-in for a repository meant to run unattended, whose grill actor is overridden to `artisan`; the Guildmaster then answers its recommendations at acceptance.

A grill step accepts the optional `skills` setting, with the shared distinct-name-list rule of other skill-bearing stages, for either allowed actor; it gains no `profile` or `instructions`, and undeclared settings stay errors ([workflow contract](../contracts/workflow.md)).
Because `workflow_overrides` changes only the actor, a workflow that wants the unattended skill lists it in the grill step's `skills`.

`guildhall-domain-model` records agreed terms and hard-to-reverse decisions in the grill notes; the Director, which only orchestrates (R1.5), never writes them into the repository, and a repository's existing glossary is read and challenged against, with additions left to the plan.
The Director makes quick read-only lookups in the repository so it never asks the Guildmaster anything it could look up; anything beyond a quick check goes to the research stage, where an Artisan does it.

## Alternatives rejected

- A self-answering grill as the default: nobody would answer, so the Artisans would build the Director's guess.
- A glossary subsystem for managed products: the project store keeps task documents and `docs/glossary.md` is Guildhall's own, so the notes hold the terms.
- Forbidding the Director to read the repository: R1.5 forbids it writing and building, not reading, and asking what it could look up defeats the grill.

## Consequences

The grill's answers live in the store, not in the managed repository.
An unattended repository trades the live interview for answering recommendations at acceptance.

## History

- 2026-10-21, D29 (0029-grill-step-skills.md): let version 1 of the `grill` step accept `skills`, so the then-shipped `guildhall-grill-notes` could run in its intended stage, validated by the same registry declaration for whole workflows and isolated render requests; it left the actor rule (`director` or `artisan`) unchanged and added no `profile` or `instructions` ([release skills root](release-skills-root.md)).
  Its tests and plan text are dropped here; the skill it served was renamed by the next record.
- 2026-10-22, D61 (0061-grill-live-interview.md): made the grill a live interview with `guildhall-grill` and `guildhall-domain-model` and kept the self-grill only as `guildhall-grill-unattended`; until the Director slice ran Director stages, an `artisan` override was the only grill the engine ran and the default grill listed no skills, which [Director stages](engine-advance.md) then changed.
