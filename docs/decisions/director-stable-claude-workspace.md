---
status: accepted
date: 2026-10-24
topic: director
---

# Claude Director sessions run in one stable workspace that the Guildmaster approves once in Claude's own trust dialog

## Context

Each Director session ran Claude in its own numbered session directory, so Claude's folder-trust dialog appeared at every start, reload and handoff, and an opt-in trust-gated stub rejected the numbered directory before consuming startup ([red run](../evidence/director-trust-2026-10-23-red.txt)).
Claude Code 2.1.309 offered no ordinary interactive trust-acceptance option: its [CLI reference](https://code.claude.com/docs/en/cli-reference) has none, `--trust-workspace` exists only for the unrelated `claude ultrareview`, and the [security documentation](https://code.claude.com/docs/en/security) describes interactive folder trust and print-mode prompt skipping.

## Decision

Claude runs in a stable folder, `<copy>/director/claude`, reused by starts, reloads and handoffs, and the Guildmaster approves it once in Claude's native dialog: the captain's route A, one-time manual approval rather than an unattended first launch ([start](../director.md#start)).
Numbered session records stay separate from the workspace, so trust can persist without overwriting session history.
The [Claude workspace contract](../contracts/adapter-claude.md#stable-director-workspace) owns publication and recovery, through the optional [`runtimes.PersistentSession`](../contracts/adapter-runtimes.md) boundary; other runtimes need no change.
The supervisor's existing admission and [runtime containment](director-runtime-record.md) serialise it, so there is no second concurrent publisher.
Folder trust and its persistence are left entirely to Claude: Guildhall does not inspect its undocumented configuration or emit a trust diagnostic.

## Alternatives rejected

- Writing Claude-owned trust state: undocumented and Claude's, not Guildhall's.
- Undocumented sandbox environment switches: not a supported trust API, and they falsely assert sandboxing.
- Permission-bypass flags, a changed permission mode or wider allow rules: they would violate the safety contract.
- Print or SDK mode in place of the interactive conversation: the Director is a conversation with the Guildmaster.

## Consequences

A Director's very first start needs the Guildmaster at the dialog; moving the copy or clearing Claude's saved trust may need approval again.
The generated `.claude` tree is refreshed from the numbered record before each launch, so it must not be edited by hand.
The permission golden is unchanged, and CI uses the stub with no model calls; an isolated named Herdr lab with real Claude confirmed one approval, then fresh restart and handoff with no further dialog ([live evidence](../evidence/director-trust-2026-10-23-live.txt)).
