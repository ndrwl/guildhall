# Retire the Guildmaster's audit of delivered tasks

[D87](0087-quality-audit.md) had deliver open a three-question `audit` decision for about one in five merged tasks ([G4](../quality.md#honesty-and-evidence-g)).
The audit is asked as the task closes, and by then the Guildmaster has forgotten how the work went: it asks for a memory they no longer have, and this project's own copy had turned it off with `quality_audit_sample = "0"`.
The Guildmaster accepted replacing it with a periodic retrospective, run by an Artisan over the Director's transcripts and the store, whose findings arrive as `accept` or `reject` decisions; this record covers the first of its three pull requests, which retires the audit.

## Decisions

- **No close opens an audit.** Deliver and the [outside-merge rule](../tasks.md#merged-outside-guildhall) no longer open one, whatever `quality_audit_sample` says. Neither close path writes anything new, so both stay replay-safe: a close that fails part-way is finished by the next advance with the same records.
- **`quality_audit_sample` is deprecated, not refused.** It is still parsed and validated, so a config that sets it, such as `"0"`, still loads, and a config's digest, taken over its parsed form, is unchanged whether or not it sets the key. It has no effect. `director start` (and `--check`) prints `config: warning: … quality_audit_sample is deprecated and has no effect …` naming its replacement, `[director] retrospective_after_merges`, which a later pull request adds. Refusing the key later, or keeping it a no-op for good, is left open.
- **The status feed keeps `quality.audit_sample`.** It is the parsed value, still 5 by default, and is documented as having no effect, so the published feed does not change shape.
- **Recorded audits stay valid.** The `audit` decision kind stays in the store's accepted kinds, so old records decode and validate. An audit already open stays answerable with `decide answer`, and prune still waits for it. The outside-merge rule still skips an open `audit` when it voids a task's decisions, so an audit an earlier release opened before a close that failed survives the replayed close. `guildhall quality agreement` still reports over the audits recorded.
- **G4's human calibration ends.** G4, A4's and G1's "Guildmaster audit" source and H2's "share the Guildmaster marked unnecessary in the G4 audit" get no new data. The retrospective does not feed H2.

## Alternatives

Voiding every open audit once on upgrade would let prune stop waiting for them, but it would throw away answers the Guildmaster may still give; leaving them answerable costs one open decision each until answered.
Reusing `quality_audit_sample` as the retrospective's cadence would silently change the meaning of a value a copy already set, such as this copy's `"0"`, so the retrospective gets a new `[director]` key instead.

Consequences: a copy gains no `audit` decision or `decision` message after a merge, and a copy that set `quality_audit_sample` sees one warning line at each Director start.
