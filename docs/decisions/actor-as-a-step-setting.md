---
status: accepted
date: 2026-10-21
topic: workflow
amended-by: review-outcome-2.md
formerly: D26, 0026-actor-as-a-step-setting.md
---

# Who fulfils a step is an actor setting, not part of the step kind's name

## Context

Review was split into `human-review` and `automated-review` kinds, so swapping who reviews meant swapping kinds, and the accepted workflow design's stage table (design report section 4.1) gives `grill` two actors, the Director in conversation with the Guildmaster or an Artisan.
No release or real task had yet bound a workflow revision or recorded a review outcome.

## Decision

`review` and `document-review` take `"actor": "artisan"` or `"guildmaster"`, and `grill` takes `"director"` or `"artisan"`.
`human-review` and `automated-review` become one `review` kind, so a repository's swap is one `actor` override that keeps the kind, wiring and every other setting, and the same contract covers design review of a document before acceptance.
A kind with a single actor takes no `actor` setting, so a definition has one spelling and one digest.
The director actor on `grill` does not conflict with R1.5, because a grill is the Director clarifying requirements with the Guildmaster, not doing the work.
`publish` is an engine step between review and validate, so forge checks have a pushed head to read; it never merges.
A review outcome's `revisions.subject` names a code head or a document digest, so an outcome counts only for the kind of thing it reviewed.
`guildhall.workflow/1` and `guildhall.review-outcome/1` changed in place, without aliases for the old kinds; a change after a release bumps the schema version instead.

## Consequences

The [configuration page](../configuration.md#built-in-step-kinds) lists each kind's actors, and `workflow_overrides` changes only a step's actor.
[Review outcome /2](review-outcome-2.md) is the schema bump this foresaw: the outcome became `guildhall.review-outcome/2`, with /1 still read, and publish also runs the review gate.
