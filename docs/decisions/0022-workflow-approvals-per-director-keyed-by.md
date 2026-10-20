# D22. Workflow approvals per Director, keyed by digest

The Guildmaster's approval of a workflow revision is a store record keyed by the definition's digest, not by name, so an edit always needs a new approval and an approval can never cover content it did not see.
Approvals belong to the Director rather than to a repository or task, so they live in a top-level `workflow-approvals` bucket created on first use; the store stays at schema 1, as in D15.
The mutation id is derived from the digest, so the store's replay and conflict rules make an approval idempotent and refuse different words for the same revision without a second check.
Definitions shipped with a release count as approved by that release without requiring a stored record, so a release that changes its default needs no migration.
See [workflow-edit authority](../configuration.md#workflow-edit-authority) for explicit approvals of shipped content and list output.
