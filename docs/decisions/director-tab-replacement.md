# Director tab replacement

Replace redundant shells rather than accumulating tabs across Director starts.
The [Director's tab](../director.md#the-directors-tab) owns the user-facing lifecycle; the [adapter contract](../contracts/adapter.md) owns starting-shell ownership and safe idle-tab closure.

Closing rather than retyping into a stopped session's shell avoids injecting a command into a pane whose user or another concurrent wrapper has started a program.
Appearance and labels are not ownership proof, so unrecorded historical shells cannot be reclaimed automatically.

The [red regression runs](../evidence/director-tabs-2026-10-23-red.txt) reproduced both the owned starting-shell leak and the stopped Director tab leak.
The [guarded live run](../evidence/director-tabs-2026-10-23-live.txt) verified one tab after first start, stopped restart, plain reload, reload `--now`, and stop in an isolated named Herdr session, with the default fleet tripwire unchanged.
The same-pane reload tests retrofit existing behavior and passed on their first run.
The shared workspace test now compares listed workspace identity without the creation-only starting-tab proof and allows closing that shell but no typed input, as the changed contract requires.
