# Lanterns

Lanterns is Guildhall's visual Claude Code plugin, shipped inside the Guildhall release and automatically installed by `guildhall director start` for every Claude Director.
There is no marketplace step and no dependency on bridge-lights or a Firstmate home.
It is separate from **[Clerk](clerk.md)**, the read-only interaction plugin that owns `/guild`, `/decisions`, `/task`, `/inspect` and `/queue`, which ships and installs beside it.
Lanterns has no Guildhall interaction commands and never changes tasks, decisions, messages or workflow state.
Artisan interactive presentation is deferred; only Artisan process labels are published today.

## Installation and support

The generated workspace contains `.claude/plugins/lanterns/`, and the Director launch command passes its absolute path with `--plugin-dir`.
Beside it, `.claude/plugins/clerk/` holds the [Clerk](clerk.md) plugin, passed with a second `--plugin-dir` after Lanterns'.
The release embeds all plugin assets in the Go binary.
A new session refreshes those assets without requiring a separate checkout, symlinks or user-level Claude settings.
The classic session-start hook uses `node` on PATH to inject the reply-marker convention.
The native status-line command and context handoff hook are Guildhall core and do not depend on Node or function hooks.

Claude Code's early-access function hooks provide reply frames and Calm.
Opt in explicitly before starting the Director:

```sh
CLAUDE_CODE_ENABLE_FUNCTION_HOOKS=1 guildhall director start --copy /path/to/copy
```

Guildhall does not set that flag for you.
Without its exact value `1`, the function-hook module does nothing: no `/calm`, preference reads, timers, redraws or frame changes.
The marker instruction and native status line remain available.
This integration is checked with Claude Code 2.1.310's zero-model-call `claude plugin test`; early-access API changes may require an adapter update.
The automated cross-platform CI also runs the engine-independent presentation contracts with Node 24.

## Reply lights and the status line

Replies open with exactly one urgency marker:

- 🔴: the reader must act now.
- 🟡: a decision is waiting.
- 🟢: no action is needed.

With function hooks enabled, each assistant text drawing keeps Claude's original content inside a rounded frame in the opening marker's color.
An unmarked reply is neutral gray, never silently green; a marker later in the text does not color the frame.
This changes presentation, not transcript text, tool execution or model context.

The workspace's one native status line runs `guildhall director context-report --lanterns`.
Its first segment is the overall light, then open tasks, running attempts, open decisions, attention items and the context percentage.
Stalled, retry, resume, recovery and released follow-up items are red; otherwise open decisions are yellow; a known healthy snapshot is green.
A decision [deferred](director.md#holds) until tasks merge is not counted while it waits.
A status-read failure is explicitly neutral (`status unavailable`), while the context figure still displays.
These counts come from Guildhall's read-only Director status, not Firstmate's counts.
Reading them does not acknowledge messages or answer decisions.

The generated status line is display only.
[Automatic handoff](director.md#automatic-handoff) reads context through the core end-of-turn transcript hook even if Lanterns is missing or disabled, or the status line is removed or replaced.
The legacy `director context-report` without `--lanterns` still supports old workspaces.

## Calm and the S2 smithy

With function hooks enabled and the session's `calm.json` beside the plugin, Calm is on by default when no preference has been saved.
`/calm` toggles the presentation preference and remembers both `on` and `off`.
It hides routine `ToolUse`, `ToolResult` and `ToolGroup` drawings, not their execution or stored transcript entries.
Assistant and user messages remain visible.
The native Spinner becomes S2, Rare forge spark: an 11-by-5-cell smithy with blue roof `▁▟███▙▁`, chimney and anvil `┳━┳`.
Smoke puffs `○ ◌ ·` rise, drift right and fade, each pose held for 440 ms on the 220 ms base tick.
The Claude-orange hearth alternates `▄` and `▁` every 880 ms.
One orange spark rises beside the anvil for ticks 8–10 of each 16-tick loop (660 ms of every 3,520 ms); otherwise there is no spark.
The blue follows the dark/light theme family; only the hearth and spark use orange, with the terminal background inherited.
Frames clip to narrow terminals without wrapping and repaint only mounted working rows.
Calm off restores native drawings and cancels its timer.

The preference is `<copy>/director/lanterns-calm`, containing `on` or `off` and a newline.
It belongs to this Director copy, survives numbered session handoffs and generated workspace replacement, and is not a `config.toml` setting.
An absent file means on; an unreadable or invalid existing file falls back to native drawings.
A failed preference write leaves the current choice unchanged and shows a transient notice.
The plugin learns where the preference is from `calm.json` in its own directory, `{"preference":"<copy>/director/lanterns-calm"}`, which the session renders with the plugin, and reads it from the plugin root rather than from the environment.
Without `calm.json`, Calm is unavailable and has no plugin-local preference fallback.

## Vitals labels

The Director supervisor merges labels into the existing vitals-axi labels file every 15 seconds, independent of model turns and function hooks.
It uses `VITALS_AXI_LABELS` when set, otherwise `$XDG_STATE_HOME/bridge-lights/vitals-identities.json` (defaulting to `~/.local/state/bridge-lights/vitals-identities.json`).

On Linux it contains vitals-axi schemaVersion 1, current boot ID and PID namespace, a generation time, an expiry at least 60 seconds after refresh and verified process selectors (`pid`, decimal-string `startTicks`, `descendants: true`).
The live Director supervisor anchors `Director <session>` and each running Artisan's recorded contained worker anchors `Artisan <repo>/<task> <step> #<attempt>`.
Direct worker matches take precedence over inherited Director labels in vitals-axi.
Stopped sessions, released owners, completed attempts and dead or reused identities are omitted on refresh.
Files are atomically replaced with private permissions; publication errors are advisory and a file expires when no publisher refreshes it.
Each copy replaces only its own copy-scoped Lanterns entries, preserving other publishers' entries and unknown fields in a live generation with the same boot and PID namespace only while their process selectors match live PIDs and start ticks.
Every successful refresh removes foreign entries whose processes exited, were reused or cannot be verified, as well as unsupported selector kinds, before applying the entry limit.
No per-publisher expiry metadata is added; a still-live matching process can remain labeled even if its original publisher stopped.
The shared expiry is never shortened; expired or incompatible generations are replaced.
Read/merge/replacement uses a nonblocking exclusive advisory flock on `<labels-file>.lock`; all concurrent publishers must honor that lock.
Lock contention, malformed files, unsupported schemas and more than 1000 merged entries leave the existing file unchanged.
The Linux-only selector schema cannot safely label Windows or macOS processes, so those platforms publish no selectors.

Existing vitals-axi invocations retain their combined view without changes to vitals-axi or manual per-copy file selection.
If using a custom `VITALS_AXI_LABELS` destination, supply the same value to the Director supervisor and the consumer.

## Checks

```sh
go test ./internal/lanterns ./internal/adapter/claude ./internal/cli ./internal/messaging
node --test internal/lanterns/plugin/tests/*.test.mjs
claude plugin test internal/lanterns/plugin
```

The last command runs only local engine tests, not model calls.
The [contracts](contracts/lanterns.md) record the read-only boundaries, failure behavior and test-first evidence.
