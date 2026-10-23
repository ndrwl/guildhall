# Stable Claude Director workspace

## Decision

Run every Claude Director session of a copy from `<copy>/director/claude`.
The operator approves Claude's native folder-trust dialog at that Director's first start; later starts, reloads and handoffs reuse the same directory identity.
This is the captain's selected route A: one-time manual approval, not an unattended first-ever launch.
Do not write Claude-owned trust state, change the permission mode, widen the allow rules, or replace the interactive conversation with print/SDK mode.

Keep rendering each numbered session into `director/session-<n>` as before, including its prompts, skills, settings, manifest, notes and endpoint.
After the preceding process has ended, publish that record's generated `.claude` tree into the stable workspace, replacing the preceding generated tree so removed release skills cannot linger.
A failed publication starts no runtime and can be retried by the next start.
The role prompt and first message name the numbered record by absolute path; the startup skill follows that exact path, not a relative file in the workspace.
The supervisor's existing admission and runtime containment own serialization; there is no second concurrent publisher.

Expose the optional `runtimes.PersistentSession` boundary only for workspace preparation; other runtimes keep their existing directories.
Leave folder-trust approval and persistence entirely to Claude, without inspecting its undocumented configuration or emitting a trust diagnostic.
Moving a copy, deleting its workspace or clearing Claude trust may require another approval.

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
