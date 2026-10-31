---
status: accepted
date: 2026-10-23
topic: director
formerly: D78, 0078-director-tab.md, director-tab-replacement.md
---

# `director start --tab` types the supervisor into one Director tab it owns, and replaces only shells it can prove it created

## Context

The Director is a long-running conversation the Guildmaster and firstmate come back to, so it needs its own visible tab in Herdr (R1.37) rather than whichever terminal ran `director start`.
The [session supervisor](director-start.md) already ran a session in the foreground, and tabs accumulated across Director starts, with each stopped session's shell and Herdr's starting shell left behind.

## Decision

- `director start --tab` opens the tab and types `guildhall director start --copy <dir>` with the tab's pane binding, quoted by `herdr.ShellCommand` ([typing the launch command](herdr-typed-launch-command.md)), so the supervisor is the tab's own foreground process and nothing Herdr-specific runs inside it except recording the binding ([the Director's tab](../director.md#the-directors-tab)).
  The wrapper waits for the session record carrying the pane, and fails once the tab's shell is back at its prompt with no such record, so a refused preflight in the tab is reported rather than waited out.
- The pane binding is passed on four flags that go together and never with `--tab`, not discovered, because a pane cannot reliably name itself across Herdr versions and shells and the wrapper already holds the ids.
- One workspace per Director, found by the label of its `name` and created without focus; workspaces carry no ownership, so two racing wrappers may each create one and the next start reuses the first listed.
- Both the wrapper and the supervisor refuse with `director: live` while the newest session is not stopped and its supervisor is alive or cannot be shown gone, whatever that state; a gone supervisor's session is recorded `stopped` as `superseded`.
- `[local] herdr_session` names the Herdr session, refused when it is `default`, so the wrapper never opens tabs in the Guildmaster's own terminal session; the tab is opt-in, and the current terminal stays `director start`'s default.
- Normal operation leaves one Director tab: Guildhall closes Herdr's starting shell in a workspace it created, and retires an earlier Director tab only when a persisted session binding proves ownership, the session is stopped or superseded, its runtime unit is empty and the tab holds one idle shell pane ([adapter contract](../contracts/adapter.md)).

## Alternatives rejected

- Retyping into a stopped session's shell: a user or another concurrent wrapper may have started a program in that pane, so closing is safer than injecting input.
- Reclaiming tabs by appearance or label: those are not ownership proof, so unrecorded shells, including those older versions left, are left for manual cleanup.
- The pane naming itself: unreliable across Herdr versions and shells.

## Consequences

Busy, split, foreign or ambiguous tabs are left untouched, and a command that fails before recording a session leaves a tab Guildhall cannot reclaim.
Closing tabs is presentation only and never changes a task or session record.
Cross-shell acceptance is a lab checklist, not CI; `director status` does not yet probe the supervisor's liveness.
A plain `director start` typed inside a Herdr pane now adopts that pane from its environment and never retires its tab ([start](../director.md#start)), which [copy discovery](copy-discovery.md) had left as a limitation.

## History

- 2026-10-22, D78 (0078-director-tab.md): put the supervisor in a tab Guildhall creates, with the passed pane binding, one workspace per Director found by label, `director: live` for `paused` and `stopping` as well as the contract's `running` and `recovering`, a configured Herdr session never `default`, and `--tab` opt-in by the Guildmaster's choice; its first rule on keeping a finished tab's output was replaced by tab replacement.
- 2026-10-23, director-tab-replacement.md: replaced redundant shells rather than accumulating tabs, closing rather than retyping and proving ownership from the recorded binding; [red runs](../evidence/director-tabs-2026-10-23-red.txt) reproduced both the starting-shell leak and the stopped-tab leak, and a [guarded live run](../evidence/director-tabs-2026-10-23-live.txt) in a named Herdr session saw one tab after start, restart, reload, `reload --now` and stop.
  The shared workspace test then compared listed workspace identity without the creation-only starting-tab proof.
