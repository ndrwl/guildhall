# Stable Claude Director workspace

## Decision

Use a stable folder identity for Claude Director sessions, with the native approval workflow documented in [Director start](../director.md#start).
This is the captain's selected route A: one-time manual approval, not an unattended first-ever launch.
Do not write Claude-owned trust state, change the permission mode, widen the allow rules, or replace the interactive conversation with print/SDK mode.

Keep numbered session records separate from the runtime workspace so trust identity can persist without overwriting session history.
The [Claude workspace contract](../contracts/adapter-claude.md#stable-director-workspace) owns publication and recovery, through the optional [`runtimes.PersistentSession`](../contracts/adapter-runtimes.md) boundary; other runtimes need no workspace change.
The supervisor's existing admission and runtime containment own serialization; there is no second concurrent publisher.
Leave folder-trust approval and persistence entirely to Claude, without inspecting its undocumented configuration or emitting a trust diagnostic.

## Investigation

The installed Claude Code was 2.1.309.
Its `--help` and the [official security documentation](https://code.claude.com/docs/en/security) describe interactive folder trust and print-mode prompt skipping.
The [CLI reference](https://code.claude.com/docs/en/cli-reference) has no ordinary interactive trust-acceptance option.
`claude --trust-workspace --guildhall-invalid-probe` fails with `unknown option '--trust-workspace'` before any model starts.
`claude ultrareview --help` documents that flag only for its unrelated cloud-review command.
Undocumented sandbox environment switches are not a supported trust API and falsely asserting sandboxing is inappropriate.
Permission-bypass flags would violate the safety contract.
A stable directory with native one-time approval is therefore the supported interactive choice.

## Evidence

The opt-in trust-gated Claude stub rejected the old numbered working directory before consuming startup ([red run](../evidence/director-trust-2026-10-23-red.txt)).
Focused tests cover successive starts, handoff plus live-supervisor reload, workspace refresh without stale skills, and unchanged permission bytes.
The existing permission golden was not changed.
The workspace-refresh unit test was added with the implementation, not claimed as a red-first test.
An isolated named Herdr lab with real Claude confirmed the first 17-rule trust dialog, native approval, then startup after fresh restart and handoff/reload without further dialogs ([live evidence](../evidence/director-trust-2026-10-23-live.txt)).
CI uses the existing stub and makes zero model calls.
