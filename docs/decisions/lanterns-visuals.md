---
status: accepted
date: 2026-10-27
topic: director
formerly: lanterns-and-core-context.md, lanterns-calm-s2.md
---

# Lanterns is a visuals-only plugin in every Claude Director workspace, with the S2 smithy and Calm on by default

## Context

The Guildmaster wanted the Director's state visible at a glance in its Claude session, and its processes labelled for vitals-axi, without a new service or a change to workflow state.

## Decision

- **Its own plugin.**
  Lanterns ships embedded in the Guildhall binary and is installed into each Claude Director's generated workspace; [Clerk](clerk-install-and-output.md), the interaction plugin, ships separately beside it.
- **Native status line and function hooks.**
  Claude's native status line carries one combined light, count and context band; function hooks draw urgency frames and Calm; early-access function hooks are never enabled implicitly ([Lanterns contract](../contracts/lanterns.md), [Lanterns](../lanterns.md)).
- **Read-only to workflow.**
  Lanterns reads Guildhall status and never changes workflow state; its only write is the Calm preference, kept outside the replaced `.claude` tree so a handoff preserves it.
- **The S2 animation.**
  The smithy silhouette with two-tick smoke poses, the hearth beat and one three-tick spark per sixteen ticks, from an eleven-cell origin centring smithy and anvil; narrower rasters clip cells rather than wrap; only the hearth and spark are orange.
- **Calm on by default.**
  With function hooks enabled and a Director copy present, an absent preference means Calm on; the command persists both choices before changing the live presentation, so an explicit off survives sessions and a failed write changes nothing.
  Sessions with no copy or without the exact function-hook flag keep native drawings and touch no preference; an invalid or unreadable preference fails closed to native drawings.
- **Vitals labels.**
  The supervisor, not the UI, publishes verified Linux Director and Artisan process labels to the existing `VITALS_AXI_LABELS` file (default under `$XDG_STATE_HOME/bridge-lights/`), merging copy-scoped entries and keeping other publishers' entries only while their selectors still match live PIDs and start ticks.
  Read, merge and atomic replacement are serialised by an exclusive flock on `<labels-file>.lock`, which every publisher must honour; the labels format is unchanged, so vitals-axi needs no change.

## Alternatives rejected

- Adding Clerk's commands to Lanterns: interaction and visuals stay separate plugins.
- Per-publisher expiry metadata for labels: dropping exited, reused or unverifiable identities on every merge keeps the format unchanged.
- The original animation and an opt-in Calm: replaced by S2 and Calm on.

## Consequences

Native status and core hooks work without function hooks; frames and `/calm` do not, and the classic marker-instruction hook needs Node on PATH.
No Artisan interactive rollout is included.
The handoff trigger's context input does not depend on Lanterns: it is the core turn-end hook's ([handoff triggers](director-handoff-triggers.md)).

## History

- 2026-10-25, lanterns-and-core-context.md: shipped Lanterns, the status-line band, function-hook frames with the original N8 animation and an opt-in Calm, and the vitals labels.
  It also moved the handoff context input off the status line into the core `turn-ended` hook, now part of [handoff triggers](director-handoff-triggers.md).
  A real `director start` on a zero-model-call Claude stub and Claude Code 2.1.310's plugin test runner verified the install, frames and four smithy blits per 880 ms.
- 2026-10-27, lanterns-calm-s2.md: adopted the S2 spark animation with per-cell colouring, replacing row-wide hearth colouring, and made Calm on by default; the pure geometry and encoding functions and the function-hook adapter stayed the integration seam, with no new platform interface.
  Fake-engine and pure-renderer contract tests were written red first, and the zero-model-call engine test checks default-on and persisted-off restoration.
