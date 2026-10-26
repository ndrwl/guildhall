# Lanterns visuals and core transcript context

## Decision

Ship Lanterns as its own visual-only plugin embedded in the Guildhall binary and installed into each Claude Director's generated workspace.
Keep the future Clerk interaction plugin separate; do not add its commands here.
Use Claude's native status line for one combined light/count/context band and function hooks for urgency frames and Calm presentation.
The original N8 animation and opt-in Calm default are superseded by the [S2 decision](lanterns-calm-s2.md); see the [Lanterns contract](../contracts/lanterns.md) for current animation and preference behavior.
Do not enable early-access function hooks implicitly.

Publish verified Linux Director and Artisan process labels to the existing `VITALS_AXI_LABELS` file, defaulting to `$XDG_STATE_HOME/bridge-lights/vitals-identities.json` (or `~/.local/state/bridge-lights/vitals-identities.json`).
The supervisor refreshes labels independently of the UI, merging copy-scoped entries while preserving other publishers' entries and fields in a live, compatible generation only when their process selectors still match live PIDs and start ticks.
Every merge drops exited, reused or unverifiable foreign process identities and unsupported selector kinds before the entry limit, without adding per-publisher expiry metadata or changing the labels format.
Serialize read/merge/atomic replacement with an advisory exclusive flock on `<labels-file>.lock`; all concurrent publishers must honor that lock.
No vitals-axi change or consumer reconfiguration is required.

Move the new workspace's automatic-handoff input off the status line into the core `turn-ended` hook.
The hook reads the latest main-session assistant usage from `transcript_path`, using input plus cache input tokens against the known Anthropic model context capacity.
The display-only `context-report --lanterns` option cannot overwrite that hook report.
Retain the original unflagged reporter for older generated workspaces.
This supersedes D85's dependence on a status-line writer, not its thresholds, crossing identities or wake behavior.
Removing or disabling Lanterns must not remove the handoff trigger.

## Constraints and consequences

Lanterns reads Guildhall status but never changes workflow state.
Its only presentation write is the persisted Calm preference; the supervisor owns advisory label publication.
The preference lives outside the replaced `.claude` tree so handoff preserves the Director's choice.
Native status and core hooks work without function hooks; frames and `/calm` do not.
The classic marker-instruction hook requires Node on PATH.
No Artisan interactive rollout or Clerk commands are included.

Transcript context uses the published [Anthropic context-window table](https://platform.claude.com/docs/en/build-with-claude/context-windows), checked on 2026-10-24.
Unknown or provider-specific model IDs do not manufacture a percentage, and malformed or unreadable transcripts cannot prevent the turn-end signal.
Reads are bounded; compaction or unavailable latest usage invalidates the prior reading and removes the persisted report after a successful transcript read.
Transient read failures leave the prior report untouched.
The known-capacity table needs maintenance as model families change; provider-specific window overrides are not inferred from billing totals or cumulative tokens.

## Evidence

The [Lanterns contract](../contracts/lanterns.md) links the red/green record.
A real `director start` using a local, zero-model-call Claude stub generated a workspace, passed its absolute Lanterns plugin directory and verified the manifest and function-hook module exist.
The installed Claude Code 2.1.310 plugin test runner verifies the actual function-hook adapter, command registration, rounded frame and four smithy blits per 880 ms.
The transcript-hook integration test crosses the 70% handoff threshold from 72% usage with neither a status line nor a plugin present and still signals on an unreadable transcript.
