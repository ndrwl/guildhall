# D83. The quality rubric ships as a document and a Director skill, each practice behind a setting

The Guildmaster accepted the engineering-quality research's six recommendations, each behind configuration so every practice stays optional.
This change ships the first two pieces: the [rubric](../quality.md) as the scored and trend layer above the review standard's S1 to S11, and the [`guildhall-self-review`](../../skills/guildhall-self-review/SKILL.md) skill that records a delivered task against it.

## The settings

The practices are switched by flat `quality_*` [settings](../configuration.md#quality-settings) rather than a new `[quality]` table, so they follow the precedence every other setting has: a Steward's managed value is read-only to the Director unless it is overridable, and a standalone copy sets them under `[local]`.
`config.Quality` reads them all, and `Parse` refuses a value it cannot use, naming the key.

- `quality_self_review`, `off` by default: the self-review is a Director skill first, not a workflow step, so it needs no engine change and its shape can change before it is bound into a workflow digest.
- `quality_size_budget_lines` and `quality_size_budget_files`, 400 and 15 by default: a soft budget that is flagged, never blocking, beside S6's "no fixed line cap", whose wording stays.
- `quality_audit_sample`, 5 by default: one in five delivered tasks for the Guildmaster's three-question audit, plus every reference run; `0` turns it off.
- `quality_reference_runs`, `off` by default, `quality_reference_runs_per_task`, 3 by default, and `quality_reference_cost_cap_usd`: real-model reference runs only for workflow-change proposals and release tags, three runs per task, under a per-evaluation cost cap that must be set before the runs can be turned on.
- `quality_judge_separate_family`, `on` by default: the judge is always a separate session and profile, and of a different model family when quota allows.
- `quality_skill_edits_need_approval`, `on` by default for the first cycle: an edit to the review, build or spec skill text is a design change that needs the Guildmaster's approval.

This initial increment consumed only the self-review switch and the size budget, through [`director status`](../director.md#status)'s `quality` section.
See [quality settings](../configuration.md#quality-settings) for the current consumers and [the audit decision](0087-quality-audit.md) for the subsequent delivery-audit increment.

## The facts script

The skill's facts script reads a unified diff and prints counts and `file:line` items for the criteria a script judges or flags: size against the budget, sleeps and skips in tests, removed assertions, secret patterns, and workflow permissions, pins and `pull_request_target`.
It is a `.sh` and `.ps1` twin using only the shell and awk, with no model call, network or third-party tool, so the self-review runs on every platform the Director does and CI tests it on a fixture diff.
Duplication, complexity and vulnerability tools such as `dupl`, `gocognit` and `govulncheck` are run only when already installed and are recorded as missing evidence otherwise, so the skill adds no dependency (S7).

## Not yet

The self-review document is written under the copy's `quality/` directory rather than recorded as a task document reference, because no command records a Director document for a delivered task.
This initial increment left model-judged scores pending with a judge brief; for the subsequent judge session, see [Running it](../quality.md#running-it).
For the remaining quality-practice work after the subsequent audit increment, see [Running it](../quality.md#running-it).
Pinning actions by SHA also remains later work.

Consequences of this initial increment: the config digest was unchanged, and self-review added a Director step after each merge only when enabled, costing one Director turn and no Artisan attempt.
The subsequent [audit decision](0087-quality-audit.md) changes the default post-merge behavior independently of self-review.
The first self-reviews held facts, gates and trends only.
