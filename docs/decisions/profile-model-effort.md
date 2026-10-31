---
status: accepted
date: 2026-10-23
topic: runtimes
formerly: D91, 0091-profile-model-effort.md, effort-default.md
---

# A profile's model and effort reach its runtime by value, effort falls back to a Guildhall setting, and a runtime refuses what it cannot honour

## Context

A Director's `[profile.<name>]` already chose a `model` and `effort`, but `launch.Process` built `artisan start` without them, so every Artisan a step launched, builders, reviewers and the judge alike, ran on its runtime's defaults whatever the profile said.
The Director's first real task needs its builders to run the model and effort the profile asks for, and a profile or Director that sets no effort needs a known one rather than the machine's Claude Code default.

## Decision

The interpreter puts the profile's model and effort on `interp.Spec` as an `adapter.Settings`, mapped unchanged to `adapter.Spec`; `launch.Process` passes them to `artisan start` as `--model` and `--effort`, each only when set, and the runtime's command passes them on: the Claude wrapper to Claude Code's own `--model` and `--effort`, the fake to a record tests read.
An unset effort falls back to `default_effort` in Guildhall's managed and local settings namespace, `medium` unless set ([configuration settings](../configuration.md#settings)), for workflow Artisan stages and the Director's own session alike.
The fallback is resolved at use, without changing the parsed tables or the config digests, so loading a separate managed file cannot freeze a fallback applied too early.
Each runtime declares what it can honour, `runtimes.Runtime.CheckSettings`, and refuses anything else with `adapter.ErrUnsupportedSetting` and the reason; preflight and session construction apply the same check, so unsupported settings never record a session.
Supported values, admission points and the fake's record are on [runtime settings](../adapters.md#runtime-settings) and the [fake Director contract](../contracts/adapter-fake.md).

## Alternatives rejected

- Passing model and effort by reference, as [allowed tools are](task-grants.md): a model and effort widen nothing, so they need no such indirection, and taking them from the profile the engine already resolved, rather than reloading the copy's `config.toml` in the wrapper, keeps the launch the engine checked the launch that runs.
- Ignoring a setting a runtime cannot honour and running on its default: a profile would silently not be what runs.
- Checking settings in config parsing: what is valid is each runtime's, and the config should not be coupled to one runtime.
- Leaving an unset effort to the machine's Claude Code defaults: the effort would differ by machine.

## Consequences

The Director's session settings still deny its direct calls to `artisan start` and the wrappers, so the flags are reached only through a profile.
Claude model validation only stops a model being read as another flag; Claude Code resolves aliases and names, and a model it does not know ends the session with no result.
The quality judge's selection rules are out of scope here.

## History

- 2026-10-23, D91 (0091-profile-model-effort.md): model and effort travel by value and a runtime refuses what it cannot honour; config parsing does not check them.
- 2026-10-23, effort-default.md: an unset effort falls back to a Guildhall `default_effort` setting, `medium`, resolved at use without mutating parsed config, rather than the machine's Claude Code defaults; explicit settings stay under the runtime's checks, and preflight and session construction stay aligned.
