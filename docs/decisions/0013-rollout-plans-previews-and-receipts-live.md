# D13. Rollout plans, previews and receipts live in a plan directory

A dry run writes `plan.json`, the per-copy previews and, after apply, timestamped receipts into one new directory named by `--plan`.
The plan digest is the sha256 of the plan's JSON encoding with its digest field set to an empty string.
See [apply](../steward.md#apply) for revalidation, machine selection, temporary transport inputs and receipt contents.
Receipts are the rollout record for now; they are not a task store.
The managed-settings source marks policy keys with `policy = true`, which is kept privately and not written to copies, because only the Guildmaster can say which preferences are review or approval rules.
