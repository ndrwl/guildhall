---
status: accepted
date: 2026-10-26
topic: artisan
---

# Generate required output contracts with every Artisan prompt

## Context

A review could approve in prose yet fail intake, because its succeeded result listed no file for the required `outcome` port (#141).
The old review guide asked only for a verdict, while the Claude result example actively showed an empty output list.
Code-head capture masked the omission for builds, because Guildhall derives Git HEAD; review outcomes cannot be derived from a summary.

## Decision

`render.Prompt` generates each Artisan kind's required port names, file paths, formats, minimal valid examples and matching result entries, independently of the replaceable stage-guide prose.
The renderer owns the complete result template; Claude supplies the result destination and fixed identity, and directs the Artisan to that template instead of duplicating an empty-output example.
The fake adapter renders no separate result instructions and receives the same generated prompt.
Review examples use the interpreter's recorded revision binding when available, not revision values guessed from the worktree or a moving configuration; standalone rendering keeps explicitly labelled illustrative revisions.
Shared stage guides describe what to produce, leaving Artisan completion mechanics to the actor-gated [generated instructions](../rendering.md#required-outputs-and-result-template).

## Alternatives rejected

- Weakening intake or inventing a review verdict: it would hide the omission rather than prevent it.
- Rewriting published attempts or recovering a live review automatically: the fix prevents future omissions only.

## Consequences

The [interpreter contract tests](../contracts/interp.md#tests) own the zero-model output-example checks and their feedback command.
Existing prompt and baseline digest goldens were intentionally updated.
Engine-only delivery and validation gain no Artisan prompts, because they run no Artisans.
