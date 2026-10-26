# Artisan workspaces and a plain live stream

## Decision

Give each workflow-launched Artisan attempt a new Herdr workspace in the Director's configured non-default session, without focus, instead of adding a tab to the Director's workspace.
Use the existing `TabHost.CreateWorkspace` boundary and owned log-streaming pane bridge; do not change Director presentation or automatically close Artisan workspaces.
Keep the attempt-local single-use claim ahead of workspace creation so uncertain terminal replies cannot duplicate a worker.

Run Claude print mode with `--output-format stream-json --verbose` and translate assistant messages and tool calls to plain lines while the subprocess runs.
Keep only the final result event for the existing usage, denial and final-text reporter, independently of earlier transcript volume.
Bound each event to 4 MiB and tool subjects to 160 runes; ignore malformed, oversized and unrelated events for display.
Display failures do not stop the worker or change result publication, containment, guard rails or retries.
The draft result file remains the completion protocol; the stream's final result event supplies accounting, not task completion.

## Consequences

Artisans are independently discoverable without displacing the Director and their progress is visible before completion.
The durable log remains plain text and survives closing the workspace.
No colour, framing, Markdown rendering, Lanterns or interactive model-session resume is added; richer presentation remains a separate stage.
A workspace created before an ambiguous reply can remain open for inspection, just as an uncertain tab did; the durable claim prevents automatic duplication.

## Evidence

The [contract-first red/green and guarded live lab evidence](../evidence/artisan-workspaces-2026-10-25.txt) distinguishes the two red-first behaviour changes from supplementary tests.
CI uses only fake runtimes and the Claude process stand-in, never a model call.
