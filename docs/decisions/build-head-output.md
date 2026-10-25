# Derive an omitted code-head output from Git

A build's final commit is its output, not a file an Artisan must invent to hold its commit id.
The dogfood D3 build succeeded and committed but returned no outputs, leaving intake unable to advance despite the stage guide saying the head was its output.

[Code-head output intake](../interpreter.md#code-head-output-intake) owns the resulting contract.
Derive omitted code-head outputs from committed work rather than asking the model to manufacture a file, while preserving explicit-file compatibility.
Refuse uncommitted tracked work so intake cannot silently omit it, but allow untracked notes and spec files because they do not change the committed code.
Use the existing bounded Git process runner rather than a shell or model call.
No speculative process interface is added for this single implementation; tests exercise real Git and persistence alongside the interpreter's existing mock-agent seam.

Preserve output identity across crashes by reusing the recorded head, and reuse existing intake recovery rather than rewriting old results or resetting their outcome.
This lets the blocked D3 task recover through normal advancement after loading the fix.

The regression was red before the implementation and green after it on both the real store and the records double, with real Git and no model calls.
It includes a finished succeeded build with empty outputs and no intake marker, untracked notes, dirty staged and unstaged work, progression to review, and replay after a later commit.
