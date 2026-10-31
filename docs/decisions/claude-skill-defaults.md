---
status: accepted
date: 2026-10-24
topic: grants
---

# Role-specific Claude skill defaults are resolved from Guildhall settings before rendering

## Context

Claude sessions loaded every bundled, account and plugin skill the machine offered, which gave each role competing instructions and behaviour that depended on the account.

## Decision

Ship `director-v1`, `artisan-v1`, `builder-v1` and `reviewer-v1` policies through Guildhall string-valued managed and local settings ([Claude skill policies](../configuration.md#claude-skill-policies)).
Profile bindings are explicit in the shipped configuration; arbitrary profile names inherit the generic Artisan policy.
The Director keeps its 13 release skills, the builder keeps claude-api, run and dataviz, and the reviewer keeps security-review only as supplementary analysis.
Researchers and planners add no bundled or account skills; the shipped researcher profile gets WebSearch and WebFetch through its [tool grant](task-grants.md), independently.
Account sync is off by default and is supplied in session flag scope to stop cached account skills from loading.
Plugin-authoring is disabled through its builtin plugin id; other plugins, including Guildhall Lanterns, are untouched.
A profile-specific effective setting replaces the generic policy, then its versioned preset expands and per-name overrides apply; existing managed locks govern each key, and invalid inactive values fail validation.
Both runtimes pass `--settings` and record a normalised policy receipt, and Director settings keep their hooks, permissions and status line.
Claude Code must be at least 2.1.310, the inspected and tested baseline; preflight and the Artisan wrapper probe only `--version`, never a model, and refuse unknown or prerelease version output.

## Alternatives rejected

- Account settings or manual edits to generated workspaces: neither is reproducible per Director or per role.
- Skill deny rules, or a blanket Skill deny: not needed for these source and plugin controls.
- The bundle kill switch to turn on a known bundle: selective suppression is used instead.

## Consequences

Skill selection reduces competing instructions; it does not sandbox shell or API tools, ambient personal or project skills, or unrelated plugins.
Unknown future bundles may load, and account opt-ins cannot suppress unknown future account names and must enable backing browser capabilities together with their safety instructions.
Organisation-managed plugin force-enablement remains outside Guildhall's lower-priority controls.
Preflight reports configured controls rather than asserting runtime discovery, and no live-model enforcement of hiding or denying is claimed: the tests run over stubs.
