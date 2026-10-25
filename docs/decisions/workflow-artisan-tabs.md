# Workflow Artisan tabs without terminal ownership

This decision introduced workflow presentation in the Director's workspace; [Artisan workspaces and a plain live stream](artisan-workspaces-readable-stream.md) supersedes that placement.
The current presentation and recovery rules live in [workflow Artisan workspaces](../artisan.md#workflow-artisan-workspaces).
The terminal remains a presentation binding, never the authority for process liveness or task state (R1.52–R1.54).

The pane runs an internal bridge that starts the ordinary launcher detached, with terminal-independent input and durable `launcher.log` output, and streams that log for observation.
Running the owned worker directly on the pane's terminal would make terminal loss a lifecycle event, so it is deliberately not an interactive model terminal.
Explicit Guildhall stop/cancel still reaches the launcher's endpoint and existing stop ladder.
`ReportAgent` registers `ResumeArgv` with source `guildhall` before typing the command; restart reconciliation never launches another worker.

An exclusive per-attempt launch intent is retained across ambiguous API responses and startup timeouts.
Ownership and files are reconciled for diagnosis before refusing a claimed/owned attempt, never inferred from a missing pane.
A claim with no owner requires operator inspection rather than automatic same-attempt retry, because a delayed terminal command may still execute.
New workflow retry attempts have their own intent.

Finished panes stay open with a clear finished line instead of automatic retirement.
This avoids both closing before landing/preservation and accidentally closing a reused or unrelated tab; all attempt records, logs and copies remain owned by Guildhall.
The existing fake-only manual tab demonstration and native Windows lab are unchanged.
