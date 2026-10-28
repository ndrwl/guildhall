# Finished Artisans' Herdr workspaces close automatically

This decision supersedes "Finished panes stay open … instead of automatic retirement" in [workflow Artisan tabs](workflow-artisan-tabs.md) and "do not … automatically close Artisan workspaces" in [Artisan workspaces and a plain live stream](artisan-workspaces-readable-stream.md).
Their other rules stand.
The current rules live in [workflow Artisan workspaces](../artisan.md#workflow-artisan-workspaces).

## Decision

A Director session's supervisor runs a **workspace close sweep** when each session starts, a reload's included, and every 5 minutes while it runs (R1.54).
The sweep is driven by durable records, never an in-memory timer, so a crash or restart still cleans up.
It closes the Herdr workspace of a finished attempt when all of these hold:

- the attempt's outcome is `succeeded`, `failed` or `stopped`, a success intake later rejected included;
- the attempt's owner is proven gone, by the same owner rule take-over uses (`reconcile.ProbeOwner(...).Gone()`), so intake has ended; an unverified owner is skipped silently;
- `artisan_workspace_close_after`, default 5 minutes, has passed since the commit time of the attempt's `finish-attempt` store receipt. `"0"` never closes automatically.

The attempt's `pane-launch.json` is the only authority, and the live Herdr state must still match it immediately before closing.
A Herdr workspace labelled one of the attempt's own labels, `└ <task> · <step> #<n>` or the `artisan <key>` that #140 to #145 gave it, closes only while it holds exactly the recorded tab, labelled `artisan <key>`, with one pane that is still the recorded one and an idle shell.
A record from before #140 names a tab in a shared Herdr workspace, possibly the Director's own: only that tab closes, through the existing idle-tab revalidation plus its label, and never a Herdr workspace whose label is not an attempt's own.
A closed or already-gone attempt gets a presentation-only `pane-closed.json` marker so later sweeps ask Herdr nothing about it.
A launching claim, an unreadable record, a record from another session or any mismatch is left open and reported once to the Director, as a `herdr-workspace-left-open` message from the `supervisor` sender, with an id derived from the attempt; it is queued unread and does not wake the Director, so the first sweep after an upgrade cannot flood it.
A later workspace close sweep still closes a left-open Herdr workspace once it matches.

## Consequences

Finished Artisans' Herdr workspaces disappear about 5 minutes after the delay without anyone closing them, including after a supervisor restart.
Closing removes only the terminal view: task, attempt, ownership and session records, git working directories, isolated copies, `launcher.log`, `events.jsonl` and result files are untouched (R1.52).
A Guildmaster still reading a finished Herdr workspace loses it when it is due; the delay is the lever, since Herdr cannot say whether anyone is watching.
Attempts recorded before containment was recorded (#31) never prove their owner gone, so their Herdr workspaces stay open, unreported.
The sweep holds the account-private workspace-order lock while closing and skips a round while it is held, so a close never interleaves with a launch's ordering.
Herdr ids are short ordinals that a restart may reuse; the full revalidation, not the id, is the safeguard.
`launch.Process` still never closes its own presentation, `director start --tab` does not sweep, and no Herdr session or server is started, stopped or deleted.
