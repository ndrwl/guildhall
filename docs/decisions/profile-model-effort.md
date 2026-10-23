# A profile's model and effort reach its runtime by value, and a runtime refuses what it cannot honour

A Director's `[profile.<name>]` already chose a `model` and `effort`, but `launch.Process` built `artisan start` without them, so every Artisan a step launched, builders, reviewers and the judge alike, ran on its runtime's defaults whatever the profile said.
The Director's first real task needs its builders to run the model and effort the profile asks for.

## By value, beside the grant reference

The interpreter puts the profile's model and effort on `interp.Spec` as an `adapter.Settings`, mapped unchanged to `adapter.Spec`; `launch.Process` passes them to `artisan start` as `--model` and `--effort`, each only when set, and the runtime's command passes them on: the Claude wrapper to Claude Code's own `--model` and `--effort`, the fake to a record tests read ([runtime settings](../adapters.md#runtime-settings)).
An unset one is not passed, so it stays the runtime's own default rather than a value Guildhall invents.

[Allowed tools travel by reference](artisan-allow-rules.md) so that no argv can widen what a session may do.
A model and effort widen nothing, so they need no such indirection, and taking them from the profile the engine already resolved, rather than reloading the copy's `config.toml` in the wrapper, keeps the launch the engine checked the launch that runs.
The Director's session settings still deny its direct calls to `artisan start` and the wrappers, so the flags are reached only through a profile.

## Refused, never ignored

Each runtime declares what it can honour, `runtimes.Runtime.CheckSettings`, and refuses anything else with `adapter.ErrUnsupportedSetting` and the reason.
The Claude adapter takes a model as one word not starting with `-`, so it is never read as another flag, and an effort among the levels Claude Code 2.1.309's `--effort` lists, `low`, `medium`, `high`, `xhigh` and `max`; it does not check that a model exists, since Claude Code resolves aliases and names itself.
The fake honours everything and records it.

The refusal comes before anything is recorded wherever the setting is known: `task new` for the workflow's profiles, the interpreter's `Engine.CheckSettings` on each launch, which `task start`, `step done` and `step retry` set, and `director start`'s preflight for every profile on its `artisans` line; the runtime's command refuses again, so `artisan start` exits before owning the attempt.
Config parsing does not check them, because what is valid is each runtime's.

The Director's own session passes `[director] model` but still not its `effort`, as before; carrying that through is a separate change.
The quality judge's selection rules are out of scope here.
