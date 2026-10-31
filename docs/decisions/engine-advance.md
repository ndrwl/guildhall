---
status: accepted
date: 2026-10-25
topic: engine
amended-by: loops-and-review-gate.md
formerly: D32, 0032-deterministic-advance-straight-path.md, build-head-output.md, D72, 0072-director-stages.md
---

# The engine advances a task deterministically from its store records, and runs Director stages as attempts

## Context

The interpreter must survive a crash at any point of an advance and be repeated without recording anything twice.
Its first slice ran only the straight path and refused loops, gates other than the spec gate, engine steps other than `deliver` and Director stages.
Later work added loops and the review gate ([loops and the review gate](loops-and-review-gate.md)), Director stages, and a build's head taken from Git when a dogfood build committed but listed no output.

## Decision

- **Position from records.**
  Every advance derives where a task stands from its store records, not from the event it was given, so a repeated advance finds the same position and repeats only what is missing.
- **One write per record.**
  Each write uses a mutation id derived from the task and the record, so a repeat replays its receipt; the writes are ordered so every prefix is one a repeat completes.
- **Record, then launch, then mark.**
  An attempt is recorded before it is launched and marked launched only after `Launch` returns, so a crash between them relaunches the same identity; refusing a live duplicate is the launcher's ownership record's job.
- **A narrow `interp.Records`.**
  The engine reads the store through an interface of only the methods it calls, with an in-memory double passing one contract suite with the real store, as [the store decision](bbolt-for-the-store.md) permits.
- **A code head is the committed work.**
  A succeeded attempt with one `code-head` port and no listed files takes its output from Git HEAD in the working directory through the existing bounded Git runner; tracked uncommitted work refuses intake, untracked files do not, and a recorded head wins on replay ([code-head output intake](../interpreter.md#code-head-output-intake)).
- **The merge decision offers `merge` and `hold`,** because the review verdicts do not describe a merge.
- **A Director stage is an attempt the Director finishes.**
  It is recorded, bound and prepared as an Artisan attempt is, handed over by a `director-stage` message instead of launched, marked `launched.json` with `launcher: director` after the message, and waits with `director-stage`; the Director's role renders its prompt, and `guildhall step finish` writes its result for the same `attempt-result` intake ([Director stages](../interpreter.md#director-stages)).
- What the engine does not run is refused with `ErrUnsupported` rather than approximated ([interpreter](../interpreter.md)).

## Alternatives rejected

- One cross-record transaction per advance: it would widen the store's interface for one consumer, and ordered single-record writes give the same recovery.
- Asking the model to write a file holding its commit id: a build's final commit already is its output.
- A process interface for Git: it would have one implementation; tests run real Git.
- A separate record kind for Director work: it would duplicate the attempt's identity, intake and replay for one actor.
- The Director writing result JSON by hand: `step finish` refuses a stale, repeated or wrong attempt and never replaces a result.

## Consequences

Outputs, outcomes, replay, `step retry`, the timeline and `director status` treat Director stages and Artisan attempts alike.
A `workflow_overrides` entry changes only a step's actor, so overriding the default grill to `artisan` renders the Director's grill skills to an Artisan; an unattended repository uses a workflow whose grill lists `guildhall-grill-unattended` ([grill](grill-live-interview.md)).
Each new step kind or loop target is added without changing what the straight path records.

## History

- 2026-10-21, D32 (0032-deterministic-advance-straight-path.md): the straight path, deterministic position, one write per record, record-before-launch, the narrow `interp.Records`, and `merge`/`hold`; loops, other gates, other engine steps and Director stages were refused; a step's first output was a file-only code-head representation, later replaced by code-head output intake.
  Rejected then: a cross-record transaction, and guessing at a live duplicate rather than leaving it to the launcher.
- 2026-10-22, D72 (0072-director-stages.md): Director stages run as attempts, handed over by message rather than launched, waiting with a fourth wait `director-stage` so the Director, `director status` and the watcher can tell a stage from an Artisan; the default grill lists `guildhall-grill` and `guildhall-domain-model`, a digest change admitted because the shipped definition is approved by the release; `step finish` copies each output into the working directory as `<port><ext>` so the existing output checks and retirement apply, and `--failed` abandons a stage, after which `step retry` hands over the next ([retry](failed-attempt-retry.md)).
  Rejected then: a separate record kind for Director work.
  Loops back to a Director stage stayed refused, and the Director's stage skill and `needs_me` items came later ([Director skills](director-skills.md)); a `changes_requested` loop to a Director stage is now taken.
- 2026-10-25, build-head-output.md: an omitted code-head output is derived from Git rather than a file the Artisan must invent, after a dogfood build committed but returned no outputs and intake could not advance; explicit head files stay compatible, existing intake recovery lets the blocked task advance without rewriting old results, and the regression ran red then green on both record backends with real Git and no model calls.
  Rejected then: asking the model to write the file, a shell or model call, and a speculative process interface.
