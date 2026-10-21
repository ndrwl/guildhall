---
name: guildhall-hallmark
description: Use after every three or four merged pull requests, or before a large slice starts, to review the whole repository for long-term cost and write ranked, PR-sized follow-ups without changing the project.
---

Adapted from `improve-codebase-architecture` and `codebase-design` in mattpocock/skills (MIT, commit 5beb35b); see CREDITS.md.

# Hallmark

A hallmark stage reads the whole repository and judges whether it stays modular, extensible and cheap to change on the way to where it is going next.
It changes nothing: your result file is a report of findings ranked by long-term cost, each fix written as one pull request with its contract and its tests.
Nobody answers you during the stage, so settle what the evidence settles and ask the Guildmaster only about changes of direction.

## When to run

Run a hallmark after every three or four merged pull requests, and before a large slice or integration boundary is joined.
A shorter cadence finds little new, and a longer one lets one shortcut become the pattern later changes copy.
Each hallmark starts from the previous one, so the reports form a ledger of how the codebase is ageing.

## Vocabulary

Use these words exactly: module, interface, implementation, seam, adapter, depth, leverage, locality.
A module is deep when much behaviour sits behind a small interface, and shallow when its interface is nearly as wide as its implementation.
The interface is the test surface: callers and tests cross the same seam.
One adapter means a hypothetical seam and two adapters mean a real one, so a new interface needs real substitution pressure.
Pure parsing, validation and projections need functions and behavioural tests, not interfaces and doubles.
The deletion test asks what would happen if a module were deleted: if complexity vanishes it was a pass-through, and if it reappears across its callers it was earning its keep.
Classify each dependency as in-process, local-substitutable, remote but owned, or true external.
Report findings in terms of locality and leverage, never "cleaner".

## Steps

1. Baseline: read the previous hallmark report first, record its commit and the current one, and list the merged pull requests and recent CI runs between them, with their failures and cancellations.
   Start a disposition table for every earlier finding: resolved, partly resolved, unchanged or worse.
   Do not report unchanged debt as a fresh discovery.
2. Destination: read the requirements, the contract rules, every recorded decision, the module documents and the plans for the next slices.
   Separate accepted direction, implemented scope, explicitly deferred work and open owner decisions, because a deferred requirement is neither a regression nor finished work.
   Weight the rest of the scan toward the modules the next slices touch and the files the recent history keeps changing.
3. Map: build the import graph and check its direction, so that lower modules never depend on higher ones and no module imports operating-system or process code it does not need.
   List every interface and function-field seam with its real adapter, its double and the shared suite both pass, or name the evidence that is missing.
   Flag a seam with one adapter, a double with no shared suite, and a double that re-implements the real adapter's rules.
   Check that valid values translate between adjacent contracts, since look-alike types can disagree on which states are valid.
4. Scan: look for shallow modules and pass-through layers, callers parsing another module's names or coordinating its internals, one concept spread across files, two owners of one launch or stop, and one rule written twice.
   Search for the usual duplicates: validators, decoders, digest helpers, atomic writers, name literals and platform branches.
   Apply the deletion test to each candidate, compare the smallest fix with at least one alternative, and prefer a private typed view to a public abstraction where nothing varies.
   Badge each candidate Strong, Worth exploring or Speculative, and keep Speculative ones off the critical path.
5. Probe: exercise one joined seam where the next slices meet, with real files and processes and scripted boundary behaviour, never model calls.
   Try the transitions, not just the happy path: a lost acknowledgement, a partial publication, a replay, an old and a new protocol, a stale identity, a surviving child process and a lock outliving its owner.
   Check the side effects before a refusal, not only that the refusal happens.
   Keep probes outside the tracked tree, keep them reproducible, and label a failure against a proposed contract apart from a regression against a current one.
6. Tests: run the suite fresh twice and with the race detector where it is supported, and list the slowest tests, the named skips, the sleeps and the platform-only files.
   Record each command's exit code on its own, never just the end of a compound command.
   Classify each recent CI failure as a code defect, a test portability problem, an infrastructure failure or a demonstrated flake.
   Find two suites for one contract and test packages that repeat the same cases, and never propose deleting a case until the retained boundary proves it.
   A check you could not run is missing evidence, not a pass.
7. Docs and build: check every "not yet", "later" and support claim in the documents against the code, compare the contract inventory with the packages, and count decision files awaiting an index entry.
   Check module tidiness and verification, CI steps against the contributing guide, tests defined but never run, the cancellation policy, action pinning, lint settings, and embedded skill credits and digests.
   When a new record or outcome type lands, check that the inspection commands show it.
8. Streams: name the parallel streams of work and the nouns they share, and find one concept with two types, two fixture trees for one contract, and one word used for several things.
9. Decisions: for every recorded decision a candidate touches, say whether to reopen it, the better option, and what changing it costs now against later, and keep the rest explicitly.
   Never re-litigate a settled decision silently.
10. Write: rank the findings by compounding long-term cost and state urgency separately, then write the report in the order and shape of `template.md`.
    Give every finding its current `file:line`, the rule or contract it touches, the concrete failure or maintenance pressure, the smallest fix, its size and its badge.
    Make every fix one landable pull request with its contract, naming operations, values, errors, identity, ownership, concurrency, replay and recovery where they apply.
    Separate new red-first tests from pins that pass against existing behaviour, and split a broad migration into add, compose and delete steps with the deletion gate named.
    Never infer a production incident from a probe or performance from file length, and keep accepted architecture and model-free CI unless the evidence earns an explicit decision.
    End with what this procedure should change next time, when a step proved useless or a probe was missing.

## Rules

- Change nothing in the project: no edits, installs, pushes or pull requests, and confirm the tracked diff is empty before you finish.
- Treat the previous report's proposals as revisable, since the right first repair may be smaller than the abstraction it suggested.
- Ask the Guildmaster numbered questions only for direction changes, each with your recommended answer, and never manufacture a question for ordinary scheduling.

The report is complete when every earlier finding has a disposition, every new finding has an evidence line, every fix has a contract and its tests, every reopened decision names its cost now and later, and the project tree is unchanged.
