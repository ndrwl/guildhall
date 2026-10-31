---
status: accepted
date: 2026-10-21
topic: runtimes
amended-by: task-grants.md
formerly: D55, 0055-claude-adapter-wrapper.md
---

# The Claude adapter runs print mode under a wrapper that owns the result

## Context

The Director goal needed a first real Claude runtime for one owned attempt.
Claude's own file tools would overwrite an existing result and know nothing of the cancel marker, so the model cannot be trusted with the conformance guarantees: never replacing a result, publishing nothing once cancelled, and leaving no result when stopped.

## Decision

The first Claude adapter runs `claude --print` for one attempt, as an owned attempt, under `guildhall claude-artisan`, a wrapper in the same binary ([Claude adapter](../adapters.md#claude-adapter)).
Claude reads a copy of the prompt and writes a draft in the attempt's model directory, the only directory it may write besides its worktree; the wrapper publishes the draft's bytes as the result by hard link once Claude exits.
The attempt directory itself, with the result, the cancel marker and the launcher's files, is never given to Claude.
The wrapper keeps the guarantees, and by stopping Claude on the marker it lets the owned launcher's stop end at rung 1.
It publishes without validating, so completion still comes only from the reader's validation of the result file.
The permission mode is `acceptEdits`, the narrowest that lets an Artisan edit its worktree and write its draft.
CI runs the conformance suite over a stub `claude` on `PATH`; a real session runs only by hand with `GUILDHALL_CLAUDE_LIVE=1`.

## Alternatives rejected

- The interactive session in a Herdr pane that R1.23 describes: print mode is the smallest real session the Director goal needs; the tab mode, transcripts, cost and resume are later work.
- Trusting Claude to write the result file itself: it cannot keep the result protocol.

## Consequences

This is less than R1.23 asks for.
Wider permissions than `acceptEdits` were first left to the Director's managed settings; [the grants record](task-grants.md) replaced that with per-Director grants that need no managed mode.
The live acceptance passed on the earlier wrapper, before the model directory, and was pending a re-run on the current wrapper when this was written.
