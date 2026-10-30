# Recorded spend report

`guildhall spend report --copy <dir> [--repo <name>] [--task <id>] [--since <RFC3339>] [--json]` reads the existing store and attempt usage files without writing, launching processes or parsing transcripts.
This first increment reports only recorded aggregate usage; the unavailable attribution dimensions are listed below.
The default is a human-readable table; `--json` returns the same complete report as JSON.
`--copy` defaults to `GUILDHALL_COPY`; alternatively, use both `--store <path>` and `--state <dir>` to select an existing store and its attempt state directory explicitly.
Unknown flags, positional arguments and invalid timestamps fail before opening the store.
An absent store is an error, not an empty report.

## Behavioural contract

Each row groups repository, task, step, recorded profile, role, adapter and model.
Every recorded attempt is counted, including running attempts whose current spend is unknown.
The store's spend takes precedence over an attempt's `usage.json`; the file is used only when the store has no spend.
Missing usage is unknown, not zero measured usage; malformed usage is an error rather than silently discarded evidence.
Token and cost provenance are independent: measured and estimated subtotals remain separate and unknown counts are explicit.
Totals are labelled partial whenever tokens or cost are unknown.
Rows are sorted by their grouping keys and an empty selection returns zero attempts explicitly.
Retries count independently, with no double counting of the store and file sources.
`--since` includes attempts started at or after the given instant, using start receipts, not finish times or file timestamps.
Attempts without an identifiable start receipt are excluded by `--since` and counted separately as `excluded_unknown_start`.

The rendered attempt manifest supplies profile and role (the recorded role source path).
A [prune](running.md#pruning) leaves spend unaffected: it keeps each attempt's `result.json`, `usage.json` and `prompt/manifest.json`.
Absent metadata is labelled `unavailable`; current configuration is never substituted for historical attribution.
Adapter and model are unavailable because neither the store spend nor the usage schema records them.
Prompt-only, tool-schema and file-read token attribution is also unavailable: input tokens are not a prompt-only counter, and file sizes are not token counts.
To support these dimensions, adapters would need to persist the actual adapter/model identity and separately measured or explicitly estimated prompt, tool-schema and file-read counters in a versioned attempt usage record.
This command performs measurement only: no pricing, tuning, budgets or dashboard.

The read-only store handle provides a stable store view while held; attempt files can still be published concurrently, so rerunning may observe newer usage.
No state or recovery markers are changed by a report.
