# Director tab replacement

A Director workspace normally has one Director tab.
When Guildhall creates a workspace, it captures the starting tab and root pane identities from Herdr's creation response and closes that owned shell only after creating a replacement tab.
An existing workspace's shells are never inferred to be owned.
The initial shell is allowed to finish its startup files before closure.
After a new Director session is recorded, the wrapper closes a previous recorded Director tab only if its session is stopped or superseded, its runtime containment is empty, and its pane shell is idle, still in the recorded tab, and that tab has only one pane.
A missing tab is harmless; ambiguous identity, busy panes and terminal errors leave the tab untouched and report a diagnostic.
Tab cleanup never writes task or session state.
Reload and handoff already restart the runtime under the same supervisor in the same pane; tests must preserve that binding for plain reload and `--now`.

Closing rather than retyping into a stopped session's shell avoids injecting a command into a pane whose user or another concurrent wrapper has started a program.
Guildhall does not touch the session's default workspace or any unrecorded historical shells.
Old unrecorded shells require manual cleanup because appearance and labels are not ownership proof.

The [red regression runs](../evidence/director-tabs-2026-10-23-red.txt) reproduced both the owned starting-shell leak and the stopped Director tab leak.
The [guarded live run](../evidence/director-tabs-2026-10-23-live.txt) verified one tab after first start, stopped restart, plain reload, reload `--now`, and stop in an isolated named Herdr session, with the default fleet tripwire unchanged.
The same-pane reload tests retrofit existing behavior and passed on their first run.
The shared workspace test now compares listed workspace identity without the creation-only starting-tab proof and allows closing that shell but no typed input, as the changed contract requires.
