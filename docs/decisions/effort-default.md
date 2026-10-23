# Default effort for Director and Artisan sessions

Use `default_effort` in the existing `[local]` / `[managed.default_effort]` settings namespace, defaulting to `medium`.
Normal managed/local precedence and locked-key refusals apply; a session's explicit `[director] effort` or profile `effort` takes precedence over this fallback, not over a managed key of the same name.
Accept exactly `low`, `medium`, `high`, `xhigh` and `max` for the default, refusing empty, padded and unknown values in either section even when overridden.
Explicit settings keep the runtime's existing validation rules, including fake's ability to record arbitrary settings.
Resolve inheritance at runtime without mutating parsed tables or config digests, so loading a separate managed file cannot freeze a prematurely applied fallback.
A Director command with a positional runtime and no `[director]` table also gets the fallback.
Claude session construction and Director preflight use the same `CheckSettings` refusal rules as profile attempts, before recording a session.
The fake Director records its effective model and effort as `fake-settings.json` in its session directory before running its script.
No model default is introduced; direct low-level adapter specs and `artisan start` without a config-resolved profile retain their existing empty-setting behavior.
