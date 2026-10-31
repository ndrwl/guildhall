---
status: accepted
date: 2026-10-31
topic: delivery
tasks: d88
amends: task-cancel.md
amended-by: stacked-prs-not-yet.md
---

# Large work is one multi-PR task: an accepted spec lists ordered slices, delivered one pull request each

## Context

A task delivered exactly one pull request, and merging it closed the task.
Work bigger than the size budget became one oversized pull request, as d86 and d87 did, or was split with `task new --depends-on` into tasks that shared no grill, spec or acceptance and were easy to lose track of: d69 closed after its first planned pull request, and its remaining work had to be recovered as d86.
The Guildmaster asked for a way to deliver large work (d88).

## Decision

A spec may list its slices under `## Slices`, a strictly parsed numbered list, one entry per pull request with a bold title, what it delivers and its expected size; a spec without one, or with one entry, is a one-slice task, unchanged ([tasks](../tasks.md#multi-pr-tasks), [glossary](../glossary.md#slice)).
The list lives in the spec, not a second plan output, so the Guildmaster's one acceptance, bound to the spec's digest, covers exactly the slices Guildhall counts.
Guildhall refuses at the plan's intake a malformed list, more than one slice under a workflow revision with no slice range, and a revision that changes or drops a merged slice's entry.

The workflow schema gains an optional `slices` range, `{"from": "build", "to": "deliver"}` in the shipped default; it must end at a deliver step and start after the step that accepts the spec, and a workflow that declares one has no step id containing `--`.
Removing or changing a range is a weakening, so the retired default without one is never admitted for a new task.
The previous default is retired as shipped, so tasks that bound it, d87, d94 and d95 among them, advance unchanged.

The engine works out the current slice from the records alone, the slices with a merge record plus one, following [the deterministic advance](engine-advance.md), and walks a view of the bound definition, `workflow.SliceView`, in which every step of the range takes the id `<step>--s<k>` from slice 2 on.
So every lookup, staleness check, spec and review gate, send-back and mutation id sees only the current slice's records, and slice 1 keeps today's names, which leaves one-slice tasks byte for byte as they were.
The qualifier is `--s<k>` rather than a dot because step ids pass through `workflow.ValidID` in the store, the attempt keys, the CLI flags and the record names, which all accept it unchanged; the range's no-`--` rule keeps it from colliding with a step id.
Slice k from 2 on runs on branch `guildhall/<task>.s<k>` (`interp.SliceBranch`), which no task can own, in a fresh worktree, `work.s<k>`, cut from main after the slice before it merged.

Only the last slice's merge closes the task: deliver, the outside-merge rule and `task reconcile` close a slice before the last by recording its merge, telling the Director, retiring its workspace and leaving the merge queue, and the same advance starts the next slice.
`task cancel` is refused only while the current slice is merging; otherwise it closes the unmerged slices' pull requests and records the task cancelled, its merged slices standing.

Slices do not use GitHub stacked pull requests, for the reasons and re-open conditions in [Slices stay sequential](stacked-prs-not-yet.md).

## Alternatives rejected

- An epic object grouping child tasks: a second kind of object in the store, Clerk and status, and the children still have separate specs and acceptances.
- Better tooling for dependent tasks: keeps the lost shared grill, spec and approval that caused d69.
- A second `slices` port of plan: the Guildmaster would approve two documents and could approve a list that differs from the prose read.
- A stored slice cursor on the task: the engine derives its position from the records, and a cursor is one more thing a crash could leave behind.
- A `.s<k>` step qualifier: every place that checks a step id with `workflow.ValidID` would need a second grammar.
- One branch for every slice: deliver squash-merges, so the old branch's commits are not ancestors of main, which breaks the base, fast-forward and head checks.

## Consequences

A multi-PR task's records carry slice step ids from slice 2 on, which every reader of record names must accept; `interp.DocumentSlice` reads a name's slice.
The engine's unit of delivery is now a slice, so a slice merge sends a `merged` message and counts toward `hallmark_after_merges`, while `handoff_after_tasks`, dependencies and `--until-merged` holds still wait for the task to close.
The size budget stays a soft flag, measured per slice pull request.
Slices run one at a time and never in parallel, and bug and hallmark workflows declare no range, so they stay one-slice.
d88 itself could not use the mechanism it adds and landed as one oversized pull request.
