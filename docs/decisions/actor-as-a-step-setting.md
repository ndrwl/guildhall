# Actor as a step setting, evolved in place

Who fulfils a step is a setting, not part of the kind's name: `review` and `document-review` take `"actor": "artisan"` or `"guildmaster"`, and `grill` takes `"director"` or `"artisan"`.
The director actor on `grill` is part of the accepted workflow design's stage table (design report section 4.1: grill produces notes, with actor director, a conversation with the Guildmaster, or artisan) and does not conflict with R1.5, because a grill is the Director clarifying requirements with the Guildmaster, not doing the work.
`human-review` and `automated-review` become one `review` kind, so a repository's swap is one `actor` override that keeps the kind, wiring and every other setting, and the same contract covers design review of a document before acceptance.
A kind with a single actor takes no `actor` setting, so a definition has one spelling and one digest.
`publish` is an engine step between review and validate, so forge checks have a pushed head to read; it never merges.
A review outcome's `revisions.subject` names a code head or a document digest, so an outcome counts only for the kind of thing it reviewed.
`guildhall.workflow/1` and `guildhall.review-outcome/1` change in place without aliases for the old kinds, because no release or real task has bound a workflow revision or recorded a review outcome; a change after a release bumps the schema version instead.
