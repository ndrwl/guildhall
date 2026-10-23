# Default effort for Director and Artisan sessions

Use the existing managed/local settings namespace for the effort fallback rather than the machine's Claude Code defaults; [configuration settings](../configuration.md#settings) owns its values, default and precedence.
Keep explicit settings under the [runtime's validation rules](../adapters.md#runtime-settings), rather than coupling config parsing to a particular runtime.
Resolve inheritance at runtime without mutating parsed tables or config digests, so loading a separate managed file cannot freeze a prematurely applied fallback.
Keep preflight and session construction aligned so unsupported settings cannot record a session; see [runtime settings](../adapters.md#runtime-settings) for admission and low-level behavior.
The [fake Director contract](../contracts/adapter-fake.md) makes the effective settings observable without model calls.
