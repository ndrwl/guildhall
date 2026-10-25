# Derive an omitted code-head output from Git

A build's final commit is its output, not a file an Artisan must invent to hold its commit id.
The dogfood D3 build succeeded and committed but returned no outputs, leaving intake unable to advance despite the stage guide saying the head was its output.

For succeeded attempts with exactly one code-head output port and no listed files, read the full HEAD commit id directly from Git in the working directory and persist a head document outside the worktree.
Keep validation unchanged for explicitly listed outputs and for other artefact types.
Reject staged or unstaged tracked changes, naming the paths, rather than silently omitting work from the output.
Allow untracked notes and spec files, which do not change the committed code.
Use the existing bounded Git process runner; no shell or model call is involved.
No speculative process interface is added for this single implementation; tests exercise real Git and persistence alongside the interpreter's existing mock-agent seam.

Reuse an already recorded head on replay, so a crash after recording the document cannot move its identity with later workspace changes.
Existing intake recovery already replays a launcher-finished succeeded attempt while `intake.json` is absent; use that path rather than rewriting old results or resetting their outcome.
D3 can therefore continue on its next advance after the fixed binary is loaded, without a retry or any hand edit of its state.

The regression was red before the implementation and green after it on both the real store and the records double, with real Git and no model calls.
It includes a finished succeeded build with empty outputs and no intake marker, untracked notes, dirty staged and unstaged work, progression to review, and replay after a later commit.
