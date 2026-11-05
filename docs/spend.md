# Recorded spend report

`guildhall spend report --copy <dir> [--repo <name>] [--task <id>] [--since <RFC3339>] [--json]` reads the existing store and attempt usage files without writing, launching processes or parsing transcripts.
It reports recorded aggregate usage split by attempt outcome; the unavailable attribution dimensions are listed below.
The default is a human-readable table; `--json` returns the same complete report as JSON.
The [November 2026 token usage audit](audits/token-usage-2026-11.md) breaks this spend down by step, outcome, day and cache behaviour, and ranks the savings.
`--copy` defaults to the copy found from the current directory ([copies](running.md#copies)); alternatively, use both `--store <path>` and `--state <dir>` to select an existing store and its attempt state directory explicitly.
Unknown flags, positional arguments and invalid timestamps fail before opening the store.
An absent store is an error, not an empty report.

## Behavioural contract

Each row groups repository, task, step, rerun label, recorded profile, role, adapter, model and outcome.
The outcome is `succeeded`, `failed` or `stopped` as the store records it, `running` for an attempt not yet finished, or `refused` for an attempt whose result intake refused; refusal is checked first, because the store records a refused attempt as failed.
Every recorded attempt is counted, including running attempts whose current spend is unknown.
The store's spend takes precedence over an attempt's `usage.json`; the file is used only when the store has no spend.
Missing usage is unknown, not zero measured usage; malformed usage is an error rather than silently discarded evidence.
Token and cost provenance are independent: measured and estimated subtotals remain separate and unknown counts are explicit.
Totals are labelled partial whenever tokens or cost are unknown.
A Codex attempt, today only the quality judge, records measured tokens and an unknown cost, because Codex reports no price; a stopped Codex run records nothing, as a stopped Claude session does.
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

## Retries and send-backs

The rerun label (`rerun` in JSON, the `RERUN` table column) says what came before an attempt, so spend on retries and send-back rounds reads straight from the report.
An attempt's predecessor is attempt N−1 of the same task and step; a sliced step such as `build--s2` is its own step.
Director attempts follow the same rule.

| Label | Attempt N | Predecessor |
|---|---|---|
| `first` | N = 1 | none |
| `after-refusal` | N > 1 | refused at intake (it has an intake rejection) |
| `after-failure` | N > 1 | failed |
| `after-stop` | N > 1 | stopped |
| `send-back` | N > 1 | succeeded: a review or validate loop, `guildhall step send-back` or an automatic conflict rerun |
| `unknown` | N > 1 | missing or still running |

A refusal is checked before the outcome, because a refused attempt is stored as failed and may replace a launcher-succeeded outcome.
Labels are computed from each task's full attempt list before `--since` applies, so a filter never changes a label: a predecessor outside the window still labels its successor but is not counted.
Splitting a task and step into one row per label leaves totals unchanged; no attempt is counted twice.
`by_rerun` maps each label that occurs to its subtotal, and these subtotals sum to `total`; the table prints them as one `TOTAL` line per label after the overall `TOTAL`.
The label names no send-back round number and no cause of a send-back.
