# Resolve role-specific Claude skill defaults before rendering

## Decision

Ship `director-v1`, `artisan-v1`, `builder-v1` and `reviewer-v1` policies through Guildhall string-valued managed/local settings, not account settings or manual generated-workspace edits.
Profile bindings are explicit in the shipped configuration; arbitrary profile names inherit the generic Artisan policy.
The Director preserves its 13 release skills, builder keeps claude-api/run/dataviz, and reviewer keeps security-review only as supplementary analysis.
Researchers and planners add no bundled/account skills, but the shipped researcher profile grants WebSearch and WebFetch independently through existing profile tool grants.
Account sync is off by default and must be supplied in session flag scope to stop cached account skills from loading.
Plugin-authoring is disabled through its builtin plugin ID; other plugins, including Guildhall Lanterns, are untouched.
No new Skill denies are required for these source/plugin controls, and no blanket Skill deny is added.

## Rationale and limits

Skill selection reduces competing instructions and account-dependent behavior; it does not sandbox shell/API tools, ambient personal/project skills or unrelated plugins.
A profile-specific effective setting replaces the generic policy, then its versioned preset expands and per-name overrides apply.
Existing managed locks govern each key, and invalid inactive values fail validation.
Turning on a known bundle requires selective suppression rather than the bundle kill switch; unknown future bundles may load.
Account opt-ins likewise cannot suppress unknown future account names and must enable backing browser capabilities together with their safety instructions.
Organization-managed plugin force-enablement remains outside Guildhall's lower-priority controls.

## Compatibility and evidence

Require Claude Code at least 2.1.310, the inspected/tested baseline, and reject unknown/prerelease version output.
Preflight and the Artisan wrapper probe only `--version`, never a model, and report configured controls rather than asserting runtime discovery.
Director settings preserve hooks, permissions and status line; both runtimes pass `--settings` and record normalized policy receipts.
New policy/resolver, adapter-rendering and preflight contract tests were run red before their implementations, then green.
Existing golden/process contracts were updated for the intentionally changed always-present settings flag and receipt; the shipped-example semantic test is regression coverage, not claimed prior red evidence.
No live-model hide/deny enforcement is claimed by these stub tests.
