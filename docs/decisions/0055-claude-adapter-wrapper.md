# D55. The Claude adapter runs print mode under a wrapper that owns the result

The first Claude adapter runs `claude --print` for one attempt, as an owned attempt, under `guildhall claude-artisan`, a wrapper in the same binary.
Claude reads a copy of the prompt and writes a draft in the attempt's model directory, the only directory it may write besides its worktree; the wrapper publishes the draft's bytes as the result by hard link once Claude exits.
The attempt directory itself, with the result, the cancel marker and the launcher's files, is never given to Claude.

Claude's own file tools would overwrite an existing result and know nothing of the cancel marker, so the model cannot be trusted with the conformance guarantees: never replacing a result, publishing nothing once cancelled, and leaving no result when stopped.
The wrapper keeps them, and by stopping Claude on the marker it lets the owned launcher's stop end at rung 1.
It publishes without validating, so completion still comes only from the reader's validation of the result file.

Print mode, rather than the interactive session in a Herdr pane that R1.23 describes, is the smallest real session the Director goal needs; the tab mode, transcripts, cost and resume are later work.
The permission mode is `acceptEdits`, the narrowest that lets an Artisan edit its worktree and write its draft.
The original assignment of wider permissions to managed settings is superseded by the [per-Director profile grant decision](artisan-allow-rules.md).
CI runs the conformance suite over a stub `claude` on `PATH`; a real session runs only by hand with `GUILDHALL_CLAUDE_LIVE=1`.
The live acceptance passed on the earlier wrapper, before the model directory, and is pending a re-run on the current wrapper.
