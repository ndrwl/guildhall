# Report recorded spend without inventing attribution

The adoption plan asks for per-role and per-step token attribution before any cost tuning.
The existing attempt store and versioned usage files record aggregate input, output, cache-creation, cache-read and USD amounts, with independent token and cost provenance.

`spend report` groups existing attempts by repository, task, step and available historical metadata.
It gives the store's recorded spend precedence over the attempt usage file, so retries remain separate and a persisted amount is never counted twice.
Measured and estimated amounts have separate subtotals; unknown counts make the report explicitly partial, including running attempts without reported usage.
The report does not reuse `SpendTotal` because that type intentionally combines measured and estimated amounts.

Historical profile and role source come from the attempt's rendered manifest, not today's mutable configuration.
Adapter and model identity are unavailable in the supported spend records; inferring them from filenames or today's profile would be misleading.
Prompt-only, tool-schema and file-read attribution likewise remain unavailable until adapters supply versioned counters with provenance.
No transcript inspection or character-to-token estimates are introduced.

Report aggregation stays within the CLI package and tests against real temporary stores and attempt files.
A narrow consumer-owned store interface lets regression tests count receipt scans and supply legacy start receipts; integration coverage still uses real stores.
The unfiltered report reads attempts without scanning receipts, and the start-time filter indexes receipts in one store-wide scan.
The existing store boundary owns locking and immutable record reads; the adapter boundary owns usage-file validation.
The report opens the store read-only and never creates a missing copy.
