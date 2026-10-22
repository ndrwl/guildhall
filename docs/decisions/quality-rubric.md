# The quality rubric ships as a document and a Director skill, each practice behind a setting

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

Only the self-review switch and the size budget are read today, by the skill through [`director status`](../director.md#status)'s `quality` section.
The audit, reference-run, judge and skill-edit settings are parsed, validated and documented now so a copy can set them, and have no behaviour until the work that consumes them lands.

## The facts script

The skill's facts script reads a unified diff and prints counts and `file:line` items for the script-judged criteria: size against the budget, sleeps and skips in tests, removed assertions, secret patterns, and workflow permissions, pins and `pull_request_target`.
It is a `.sh` and `.ps1` twin using only the shell and awk, with no model call, network or third-party tool, so the self-review runs on every platform the Director does and CI tests it on a fixture diff.
Duplication, complexity and vulnerability tools such as `dupl`, `gocognit` and `govulncheck` are run only when already installed and are recorded as missing evidence otherwise, so the skill adds no dependency (S7).

## Not yet

The self-review document is written under the copy's `quality/` directory rather than recorded as a task document reference, because no command records a Director document for a delivered task.
Model-judged scores are left pending with a judge brief, because nothing launches a judge session outside a workflow step yet; the Director never scores its own task.
The reference task set and its runner, the audit decision kind, the lesson-proposal shape that applies `quality_skill_edits_need_approval` to edits of the review, build and spec skills, and pinning actions by SHA are later work.

Consequences: a copy that turns nothing on behaves exactly as before, and its config digest is unchanged; a copy that turns self-review on gains a Director step after each merge that costs one Director turn and no Artisan attempt.
A model-judged score stays pending until the judge session lands, so the first self-reviews hold facts, gates and trends only.
