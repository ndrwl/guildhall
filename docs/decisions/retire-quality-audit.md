---
status: accepted
date: 2026-10-30
topic: quality
tasks: d40
amends: quality-rubric.md
amended-by: closed-tasks-have-no-open-decisions.md
formerly: D87, 0087-quality-audit.md
---

# No close opens the Guildmaster's audit of a delivered task, and recorded audits stay answerable

## Context

Deliver opened a three-question `audit` decision for about one in five merged tasks, to measure the model judge's drift ([G4](../quality.md#honesty-and-evidence-g)).
The audit is asked as the task closes, and by then the Guildmaster has forgotten how the work went: it asks for a memory they no longer have, and this project's own copy had turned it off with `quality_audit_sample = "0"`.
The Guildmaster accepted replacing it with a periodic retrospective, run by an Artisan over the Director's transcripts and the store, whose findings arrive as `accept` or `reject` decisions; this record covers the first of its three pull requests, which retires the audit.

## Decision

- **No close opens an audit.**
  Deliver and the [outside-merge rule](../tasks.md#merged-outside-guildhall) no longer open one, whatever `quality_audit_sample` says.
  Neither close path writes anything new, so both stay replay-safe: a close that fails part-way is finished by the next advance with the same records.
- **`quality_audit_sample` is deprecated, not refused.**
  It is still parsed and validated, so a config that sets it still loads and its digest is unchanged; it has no effect.
  `director start` (and `--check`) warns that it is deprecated, naming its replacement, `[director] retrospective_after_merges`.
  Refusing the key later, or keeping it a no-op for good, is left open.
- **The status feed keeps `quality.audit_sample`**, the parsed value, documented as having no effect, so the published feed does not change shape.
- **Recorded audits stay valid.**
  The `audit` kind stays in the store's accepted kinds; an open audit stays answerable with `decide answer` and prune still waits for it.
  The outside-merge rule still skips an open `audit` when it voids a task's decisions, so an audit opened before a close that failed survives the replayed close.
  [d121](closed-tasks-have-no-open-decisions.md) amends this: every close now voids every open decision, an audit included, so an audit on a task still open is voided when it closes, while one an earlier release left open on a done task stays answerable.
  `guildhall quality agreement` still reports over the audits recorded.
- **G4's human calibration ends.**
  G4, A4's and G1's "Guildmaster audit" source and H2's "share the Guildmaster marked unnecessary" get no new data; the retrospective does not feed H2.

## Alternatives rejected

- Voiding every open audit once on upgrade: prune could stop waiting, but it would throw away answers the Guildmaster may still give; leaving them answerable costs one open decision each.
- Reusing `quality_audit_sample` as the retrospective's cadence: it would silently change the meaning of a value a copy already set, such as this copy's `"0"`.

## Consequences

A copy gains no `audit` decision or `decision` message after a merge, and a copy that set `quality_audit_sample` sees one warning line at each Director start.
The judge's agreement with the Guildmaster ([quality rubric](quality-rubric.md)) gets no new data.

## History

- 2026-10-23, D87 (0087-quality-audit.md): deliver opened the audit as it closed a merged task, before marking it `done`, so the sample was chosen by code rather than a model remembering to count, and a failed close replayed to the same audit; the audit never held a task.
  It was a decision of its own kind, `audit`, with id `audit`, which cannot collide with a step's `<step>-<round>`; a `question` decision was rejected because the audit would be indistinguishable in the timeline and the H2 counts.
  The first question took `yes` or `no` as the answer's first line, so agreement could be counted from `choice`; the other two were the note in the Guildmaster's own words.
  `quality_audit_sample` alone controlled it, independently of self-review: 0 off, 1 every task, 5 by default, chosen by a deterministic hash of the task's identity ([config contract](../contracts/config.md)), so a replayed close gave the same answer with no counter and no race; an exact every-Nth counter was rejected because it needs a store record updated in the close's transaction.
  Reference runs were not sampled, since no reference runner existed.
  This record retired it, for the reasons above.
