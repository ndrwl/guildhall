# Claude skill policy contract

`config.ClaudeSkills(profile)` resolves a typed, versioned policy before any session is rendered.
An empty profile selects `claude_skills_director`; other profiles select `claude_skills_profile_<name>` when declared, otherwise `claude_skills_artisan`.
Absent settings use `director-v1` or `artisan-v1`, never guess a role from a profile name.
Managed/local replacement and locking use the existing setting contract.
Every supplied policy is validated, including overridden values: reject unknown fields, duplicate keys, invalid names/states, unsupported presets and account keeps without account syncing.
Profile-specific policy on a non-Claude adapter is an error.
Presets keep Guildhall release and workflow skill documents independent of external skill selection.
Builder keeps claude-api, run and dataviz; reviewer keeps security-review as a supplement; other presets disable bundles.
All presets disable account sync and only the plugin-authoring builtin plugin, preserving unrelated plugins including Lanterns.
Selective bundled policies suppress known other bundles and explicitly cannot promise suppression of unknown future bundles.
Unknown custom names are accepted but diagnosed, not claimed discovered.
Resolution is pure, deterministic and owns no account files, processes or mutable global state.
The normalized policy digest records the effective controls, with source key, precedence and lock provenance separate.

The Claude adapter merges controls into its owned settings without removing hooks, status line or permission rules.
Every Artisan receives flag-scoped settings even with empty grants, outside model-writable added directories.
Director settings are likewise supplied with `--settings` so account sync uses a supported scope.
An owned policy receipt is included in generated session/attempt files.
The process boundary checks Claude Code version before launch; versions older than 2.1.310 or unparseable versions refuse rather than silently degrade.
Preflight performs the same check without a model call, validates the generated settings document's explicit switches and normalized control digest, and reports configured controls, inventory uncertainty and ambient limitations, not live availability.
Reload re-resolves and rechecks the policy before recording the next session.
Existing process/filesystem integration tests remain; all automated tests use process stubs and no model calls.

Tests: `internal/config/claude_skills_test.go`, `internal/adapter/claude/skill_policy_test.go`, and `internal/cli/skill_policy_test.go`.
