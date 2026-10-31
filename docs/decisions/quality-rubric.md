---
status: accepted
date: 2026-10-23
topic: quality
amended-by: retire-quality-audit.md
formerly: D83, 0083-quality-rubric.md, quality-judge.md
---

# The quality rubric ships as a document and a Director skill, each practice behind a setting, with model scores from a separate judge attempt

## Context

The Guildmaster accepted the engineering-quality research's six recommendations, each behind configuration so every practice stays optional.
Model-judged scores were to come from a separate judge session, never the builder's or the Director's, of a different model family when quota allows.

## Decision

- The [rubric](../quality.md) is the scored and trend layer above the review standard's S1 to S11, and the [`guildhall-self-review`](../../skills/guildhall-self-review/SKILL.md) Director skill records a delivered task against it, under the copy's `quality/` directory.
- The practices are switched by flat `quality_*` [settings](../configuration.md#quality-settings), so they follow the precedence of every other setting; `config.Quality` reads them and `Parse` refuses a value it cannot use, naming the key.
  The size budget (400 lines, 15 files) is soft: flagged, never blocking, beside S6's "no fixed line cap".
  Skill text edits to review, build or spec need the Guildmaster's approval by default.
- The skill's facts script is a `.sh` and `.ps1` twin using only the shell and awk, with no model call, network or third-party tool; `dupl`, `gocognit` and `govulncheck` run only when installed and are recorded as missing evidence otherwise.
- `guildhall quality judge` launches the judge as an owned attempt of the reserved step `quality-judge` on a `done` task, through the same launcher, caps and pause hold as every Artisan attempt ([quality contract](../contracts/quality.md), [running it](../quality.md#running-it)).
  `workflow.Validate` refuses the step id, and it is the only step the store starts on a task that is not open.
  The judge writes `judgement.json`, which `quality judged` records as the task document `quality-judge-<n>-judgement`; the task stays `done`.
- `quality_judge_profiles` lists the profiles a judge may run as, in order; the builder's profile is excluded, and a profile whose judge of the same self-review failed is passed over, so "when quota allows" is measured.
  A family is a profile's adapter.
- `guildhall quality agreement` is recomputed from the recorded judgements and `audit` decisions, never stored a second time.
- `task new --fixes <repository>/<task>` records the task a fix repairs, and publish writes the line rework (C5) and change failure (E2) count from.

## Alternatives rejected

- A `[quality]` table: it would not follow the precedence every other setting has.
- The self-review or the judge as a workflow step: the self-review is a Director skill first, so its shape can change before it is bound into a workflow digest.
- The judge as a process of its own: every model process Guildhall starts is owned, contained, counted against the guard rails and has its spend recorded.
- `quality_judge_profiles` as a switch: the switches say whether and how independently to judge; only the copy knows which profiles are fit to judge.

## Consequences

Self-review costs one Director turn after each merge when enabled and no Artisan attempt; the self-review document is not a task document reference, because no command records a Director document for a delivered task.
Judging consumes Artisan attempts, retries included, counted against the guard rails and in the task's spend, so its E5 cost includes judging.
A copy that never judges gains nothing and its config digest is unchanged, but cannot start a judge until it lists an eligible profile; a workflow naming a step `quality-judge` is refused.
Position-swap consistency is not computed, since only reference evaluations make pairwise comparisons; reference runs and pinning actions by SHA remain later work.
The Guildmaster's audit that agreement pairs with is retired ([retire the audit](retire-quality-audit.md)).

## History

- 2026-10-22, D83 (0083-quality-rubric.md): shipped the rubric, the self-review skill, the `quality_*` settings and the facts script; only the self-review switch and the size budget were consumed, through `director status`, and model-judged scores were left pending with a judge brief.
  It set `quality_audit_sample` (5) for the Guildmaster's three-question audit, `quality_reference_runs` (off, three runs per task, only for workflow-change proposals and release tags) under a cost cap that must be set before they can be turned on, and `quality_judge_separate_family` (`on`).
  The audit setting was later deprecated by [retire the audit](retire-quality-audit.md).
- 2026-10-23, quality-judge.md: added the judge attempt, `quality_judge_profiles`, `quality agreement` and the `--fixes` line, as above.
  The builder is the profile of the task's latest succeeded build attempt, read from its prompt manifest in finish-receipt order; since that does not prove the historical adapter, the builder's family is now treated as unknown ([running it](../quality.md#running-it)).
  Agreement paired judgements with answered audits; since the audit was retired it counts only the audits already recorded.
