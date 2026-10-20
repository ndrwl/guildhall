# D24. Inspection reads from store receipts

The Director's inspection API starts with reads over the store: `task timeline` and `task files` (see [inspection](../tasks.md#inspection)).
Their times and durations come from receipts rather than new timestamp fields on records, because every mutation already commits a receipt with a sequence and a commit time in the same transaction as its records.
To attribute a receipt to an attempt, decision or document, receipts gain an optional record name; it is outside the request digest, so existing receipts replay unchanged, decode with no record and the store stays at schema 1.
A read is labelled with the newest committed sequence and the task's workflow revision rather than the time it ran, so two reads of the same store state print the same output and a reader can tell whether anything changed between them.
The task's receipts are found by scanning every receipt rather than through a second index, so the file layout is unchanged; an index can be added if a Director's store grows large enough to need it.
Diff, process and transcript reads need worktree custody and the adapter, and are left to that work.
