---
status: accepted
date: 2026-10-28
topic: herdr
tasks: d16
formerly: workflow-artisan-tabs.md, artisan-workspaces-readable-stream.md, artisan-child-workspaces.md, artisan-workspace-close-sweep.md
---

# Each workflow Artisan gets its own Herdr workspace below its Director, streams plain output, and closes once finished

## Context

Workflow Artisans ran as hidden detached processes, so the Guildmaster could not watch them (#136).
A Herdr pane is only a presentation binding, never the authority for process liveness or task state (R1.52 to R1.54): losing a terminal must not become a lifecycle event, and a missing pane must not become permission to start another worker.
Tabs in the Director's workspace displaced the Director (#140), new workspaces landed away from the Director that launched them (#146), and finished workspaces piled up until someone closed them by hand (d16, #161).

## Decision

With a Director's `herdr_session` set, each workflow attempt opens a new Herdr workspace in that non-default session, without focus, labelled `└ <task> · <step> #<attempt>`; its tab keeps `artisan <key>`.
The pane runs an internal bridge that starts the owned launcher detached, with no terminal input and its output appended to `launcher.log`, and streams that log: a watchable print-mode Artisan, not an interactive model session.
`artisan stop` and task cancel still reach the launcher's endpoint and stop ladder; `ReportAgent` registers `ResumeArgv` with source `guildhall` before typing the command, and restart reconciliation never launches another worker.
An exclusive attempt-local `pane-launch.json` claim is written before the workspace is created and kept across ambiguous replies and startup timeouts; a claim holding a binding but no owner needs operator inspection, not a same-attempt retry, because a delayed typed command may still run.
Claude runs with `--output-format stream-json --verbose`, and assistant messages and tool calls become plain lines while it runs.
Only the final result event feeds usage, denial and final-text reporting; events are bounded to 4 MiB and tool subjects to 160 runes, display failures never touch the worker, and the draft result file stays the completion protocol.
Under an account-private named-session lock, the new workspace is recorded in a presentation-only journal and moved once, with protocol 16's `workspace.move`, to the end of its Director's contiguous child block, found from the Director's exact stored pane binding verified live.
The whole returned order is verified and only the new workspace moves; a missing capability, held lock, ambiguity or failed check warns and skips ordering, with no retry, no rollback and no failed launch.
The Director session supervisor runs a **workspace close sweep**, driven by durable records, at each session start, a reload's included, and every 5 minutes.
It closes a finished attempt's Herdr workspace once its outcome is `succeeded`, `failed` or `stopped`, its owner is proven gone by `reconcile.ProbeOwner(...).Gone()`, and `artisan_workspace_close_after` (default 5 minutes, `"0"` never) has passed since its `finish-attempt` receipt.
It closes only what `pane-launch.json` records and the live Herdr state still matches; anything else stays open and is reported once as an unread, non-waking `herdr-workspace-left-open` message from `supervisor`, and a later sweep closes it once it matches.
[Workflow Artisan workspaces](../artisan.md#workflow-artisan-workspaces) holds the full rules, the [Claude adapter](../adapters.md#claude-adapter) the stream's contents, and the [Herdr contract](../contracts/herdr.md) the ordering checks.

## Alternatives rejected

- Running the worker directly on the pane's terminal: terminal loss would become a lifecycle event.
- Tabs in the Director's workspace: they displaced the Director.
- Finding the parent Director from labels or focus: a guess, where the stored binding is exact.
- Retrying or rolling back a move whose outcome is uncertain: it could move workspaces Guildhall does not own.
- Leaving finished panes and workspaces open for good: it avoided closing before landing or closing a reused tab, but they piled up; the sweep's revalidation now covers both risks.
- An in-memory close timer: a crash or restart would leave workspaces open for ever.

## Consequences

Artisans are discoverable beside their Director without displacing it, and their progress is visible before they finish.
Closing, interrupting or losing Herdr removes only the view: task, attempt, ownership and session records, working directories, copies, `launcher.log`, `events.jsonl` and result files are untouched (R1.52).
No colour, framing, Markdown rendering, Lanterns or interactive resume is added; richer presentation is a later stage.
A workspace created before an ambiguous reply can stay open for inspection; the durable claim prevents duplicate work.
A Guildmaster still reading a finished workspace loses it when it is due; the delay is the lever, since Herdr cannot say whether anyone is watching.
Attempts recorded before containment was recorded (#31) never prove their owner gone, so their workspaces stay open, unreported.
The sweep holds the workspace-order lock while closing and skips a round while it is held; Herdr ids are short ordinals a restart may reuse, so full revalidation, not the id, is the safeguard.
`launch.Process` never closes its own presentation, `director start --tab` does not sweep, and no Herdr session or server is started, stopped or deleted.

## History

- 2026-10-24, `workflow-artisan-tabs.md` (#136): opened each workflow Artisan as a tab in the Director's workspace, through the detached launcher bridge with the launch claim and no-retry rules above, and left finished panes open with a finished line rather than retiring them, to avoid closing before landing or closing a reused or unrelated tab.
  Its placement was replaced by workspaces in #140 and its keep-open rule by the close sweep; its other rules stand.
  Evidence: [artisan tabs](../evidence/artisan-tabs-2026-10-24.txt).
- 2026-10-26, `artisan-workspaces-readable-stream.md` (#140): moved each Artisan into its own workspace in the Director's session, without focus, through `TabHost.CreateWorkspace`, instead of a tab that displaced the Director, and added the plain live stream from Claude's `stream-json` output; it kept workspaces open after finishing, which the close sweep later replaced.
  Evidence: [red/green and live lab](../evidence/artisan-workspaces-2026-10-25.txt); CI uses only fake runtimes and the Claude process stand-in.
- 2026-10-26, `artisan-child-workspaces.md` (#146): added the child label and ordering below the Director's exact stored binding, the presentation-only journal that grants no cleanup or recovery, and warn-and-skip on any doubt, rejecting guessing the parent from labels or focus and retrying or rolling back an uncertain move.
  Its unrelated rule on addressing the user moved to [the Guildmaster address](guildmaster-address.md).
- 2026-10-28, `artisan-workspace-close-sweep.md` (#161, d16): superseded the keep-open rules of the two records above with the crash-safe close sweep, rejecting an in-memory timer; it reports what it leaves open without waking the Director, so the first sweep after an upgrade cannot flood it, and still closes workspaces labelled `artisan <key>` by #140 to #145 and lone tabs from records before #140, never a shared Herdr workspace.
