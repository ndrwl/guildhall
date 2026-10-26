# `internal/config`

## Contract

`Parse` refuses unknown keys, newer schemas and credentials, applies defaults and validates (`docs/configuration.md`).
Repository paths follow the owner-machine contract (D11): POSIX-rooted and rooted Windows drive-letter paths are accepted on every host; relative, drive-relative and backslash-prefixed UNC paths are refused.
Duplicate comparison normalizes dot segments and, for drive paths, separators and drive-letter case independently of the parsing host.
`EncodeManaged` and `ParseCombined` frame and verify the separate managed file and refuse inline managed settings beside it (D14).
`Load` follows the filesystem fallback and error rules in [configuration](../configuration.md).

`Profile` and `CheckProfiles` refuse an unknown profile name (`ErrUnknownProfile`).
`DefaultAdapter` reads the explicit `default_adapter` managed/local setting, empty when unset, with ordinary precedence and no fake fallback.
`Parse` refuses an empty or non-id `default_adapter` in either section even when overridden.
`CheckArtisanAdapters` checks profile references, then refuses the first Artisan step without a profile when its supplied default adapter is empty (`ErrMissingAdapter`), naming the workflow, step and configuration remedies.
It is read-only, does not launch anything, and does not require an adapter for other actors.
Explicit fake profiles and defaults remain valid.
A profile's `AllowedTools` is its `allowed_tools` list in order, empty by default; `Parse` refuses an entry `CheckAllowedTool` refuses, a tool name with an optional non-empty parenthesised specifier and no control characters that does not allow every shell command (`Bash`, `Bash(*)`, `Bash(:*)`), and a duplicate, naming the profile; profiles are read only from the copy's own config, never from `managed.toml`.
`StallInterval` is the effective `stall_interval` [setting](../configuration.md#settings) as a duration, `DefaultStallInterval` (15 minutes) when unset and zero for `0s`; `Parse` refuses a managed or local value that is not a nonnegative Go duration, and `StallInterval` returns that error for a config built without `Parse`.
`ChecksRefresh` is the effective `checks_refresh` setting the same way, `DefaultChecksRefresh` (1 minute) when unset and zero for `0s`.
`LockWait` is the effective `lock_wait` setting, `DefaultLockWait` (30 seconds) when unset; `Parse` refuses a managed or local value that is not a positive Go duration, and `LockWait` returns that error for a config built without `Parse`.
`GuardRails` returns the [guard rails](../director.md#guard-rails): the listed repositories in config order, their merge policies, and the effective `max_running_attempts` and `max_attempts_per_day` settings, `DefaultMaxRunningAttempts` (4) and `DefaultMaxAttemptsPerDay` (40) when unset; `Parse` refuses a managed or local cap that is not a positive whole number written plainly, and `GuardRails` returns that error, naming the key, for a config built without `Parse`.
`Quality.Audits` says whether a delivered task is sampled for the Guildmaster's audit: never when the sample is 0, every task at 1, independently of self-review, and otherwise when the first eight bytes of SHA-256 of `<repository>/<task>`, interpreted as an unsigned big-endian integer, are 0 modulo the sample, so the same task always gets the same answer ([decision](../decisions/0087-quality-audit.md)).
`Quality` returns the effective [quality settings](../configuration.md#quality-settings), each its `DefaultQuality…` value when unset, with `skill_edits_need_approval` parsed for the later lesson-proposal work and read by nothing yet, and `judge_profiles` the declared profiles `quality_judge_profiles` lists, comma-separated with optional spaces; `Parse` refuses a managed or local value that is not `on` or `off` for a switch, not a whole number written plainly for a budget or the audit sample, not a positive whole number for runs per task, not `off` or `on-change-and-release` for reference runs, or not a positive dollar amount with at most two decimals for the cost cap, naming the key, and refuses effective reference runs with no cost cap and judge profiles that are not profile names, are named twice or are not declared; `Quality` returns the same errors for a config built without `Parse`.
`Quality.Judge` chooses a [judge session](../decisions/quality-judge.md)'s `JudgeChoice`, a family being an adapter: excluding the builder's profile and failed profiles, and refusing with `ErrNoJudge` and a reason when no eligible profile is listed (including an unset list), the first listed profile of an adapter other than the builder's while `JudgeSeparateFamily` is on, else the first listed; `SeparateFamily` is true only when the builder's adapter is known and differs, and `Reason` says why it is not: none of another family listed, every one of another family failed, the setting off, or the builder unknown.
`ListedRepository` returns a listed repository or an error wrapping `ErrRepositoryNotListed` that names the id; every command acting on a repository finds it there.
`HerdrSession` is the effective `herdr_session` setting, `""` when unset; `Parse` refuses a managed or local value that is `default` or not a Herdr session name.
A repository's `NoCI` is its `no_ci` setting, `false` by default.
`Director` is the optional [`[director]` table](../configuration.md#the-director-table): an adapter that is an id when set, a config-relative role path, an `idle_after` that is a positive Go duration when set, and none in a Steward config; it is omitted from JSON when unset, as `idle_after` is, so a config without them keeps its digest.
`IdleAfter` returns the effective `[director] idle_after`, `DefaultIdleAfter` (5 minutes) when unset.
`Handoff` returns the effective [automatic handoff](../director.md#automatic-handoff) triggers: `handoff_after_tasks` (0, off, when unset), `handoff_context_percent` (`DefaultHandoffContextPercent`, 70, when unset; 0 is off) and `handoff_idle` (`0s`, off, when unset); `Parse` refuses a negative task count, a percent outside 0 to 100 and an idle period that is not a non-negative duration.

`EffectiveEffort(explicit)` returns a nonempty explicit Director/profile effort unchanged, otherwise the effective `default_effort` managed/local setting, or `medium` when absent.
It does not mutate the config or inject defaults into its digest; it is for validated configs.
`Parse` and `ParseCombined` refuse either section's `default_effort` unless it is exactly `low`, `medium`, `high`, `xhigh` or `max`, even if overridden; normal managed/local precedence applies.
Explicit session/profile settings remain runtime-validated, so fake accepts any value while Claude refuses unsupported settings before launch.

`ClaudeSkills` resolves typed, versioned managed/local skill policies with explicit profile binding, preset expansion and deterministic control digests.
See the focused [Claude policy contract](config-claude-skills.md) for validation, source/plugin limits and rendering ownership.

## Tests

`artisan_test.go` covers explicit fake/Claude/mock defaults, explicit fake profiles, unknown profiles, managed/local precedence, malformed defaults and admission of the parsed shipped example with a real runtime.
These focused configuration tests were added after the implementation; CLI regressions drove the change red first.

`claude_skills_test.go` was run red before implementation (missing parser/resolver methods), then green; `claude_defaults_test.go` adds semantic shipped-example, full matrix and identity regression coverage afterward.

`effort_test.go` was written before implementation: default/local/managed/overridden effort, explicit override, malformed values and separate managed-file precedence; its first run failed to compile because `EffectiveEffort` did not yet exist, then passed with the implementation.
`config_test.go`, `managed_test.go`, `profile_test.go`; real temporary files: `load_test.go`.
`TestProfileAllowedTools` and `TestInvalidAllowedToolsRefused` were written red first against a `CheckAllowedTool` that accepted everything ([red run](../evidence/artisan-allow-rules-2026-10-22-red.txt)); the parse, duplicate and wrong-type cases passed against it.
`stall_test.go` was written red first against a stub `StallInterval` ([red run](../evidence/stall-adapter-choice-2026-10-22-red.txt)): the default, local, zero, managed and overridden values, and refusal of values that are not nonnegative durations.
`checks_refresh_test.go` pins the same cases for `ChecksRefresh`, and `TestDirectorConfig` the `no_ci` default and value; they were added with the implementation and were not run red.
`lock_wait_test.go` was written red first against a stub `LockWait` that returned the default and a `Parse` that checked nothing ([red run](../evidence/h12-endpoint-handed-lockwait-2026-10-23-red.txt)): the default, local, managed and overridden values, and refusal of values that are not positive durations.
`herdr_session_test.go` was written red first against a stub `HerdrSession` ([red run](../evidence/director-tab-2026-10-22-red.txt)): unset, local and managed values, and refusals of `default` and malformed names.
`quality_test.go` was written red first against a stub `Quality` ([red run](../evidence/quality-rubric-2026-10-22-red.txt)): the defaults, each setting set locally, managed and overridden, a managed cost cap beside local reference runs, and the refusals of each malformed value in both sections and of reference runs with no cap; the `quality_skill_edits_need_approval` cases were added after that run and were not run red.
`TestQualityJudgeProfiles` and `TestQualityJudge` were written red first against a stub `Judge` and an unparsed `quality_judge_profiles` ([red run](../evidence/quality-judge-2026-10-23-red.txt)): unset, ordered and refused profile lists, and each choice and reason of the decision's table.
`TestQualityAudits` checks no task at sample 0 and every task at 1 with self-review both off and on, and at 5 the same answer on repeat calls and across both self-review settings for each of 2000 tasks, with between 300 and 500 chosen.
The initial stub-based [red run](../evidence/audit-decision-2026-10-23-red.txt) predates the removal of the self-review prerequisite.
`director_test.go` pins the `[director]` table's parse and refusals, `idle_after` and its default among them; it was added with the implementation and was not run red.
`TestDirectorHandoffTriggers` was written red first against a stub `Handoff` ([red run](../evidence/director-handoff-triggers-2026-10-22-red.txt)): the defaults, set values, every trigger off, and each refusal.
`guard_rails_test.go` was written red first against stub `GuardRails` and `ListedRepository` ([red run](../evidence/guard-rails-2026-10-22-red.txt)): the defaults, local, managed and overridden caps, the listed ids and merge policies, refusal of caps that are zero, negative, words, fractions or padded, and the named refusal of an unlisted repository.
