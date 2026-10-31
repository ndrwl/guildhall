---
status: accepted
date: 2026-10-21
topic: review
formerly: D42, 0042-rebase-classification.md
---

# Rebase classification runs the git program through an internal/git boundary

## Context

Rebase-aware re-review (director slice S6b part 3, steering 029) needs `git range-diff` to tell whether a new commit range is a rebase of the reviewed one.

## Decision

Guildhall gains its first git boundary, `internal/git`, which runs the git program with argument lists.
The classifier answers one question, whether a new commit range is a rebase of the reviewed one, and is conservative: every check must pass for a narrowed review, and any failure or doubt, including a git error, is a full review with a reason rather than an error.
A base change to a file the task touches is a full review.
The commit ranges are explicit inputs; the engine wiring supplies the base and decides when to ask, and a missing base means a full review.
The [git contract's tests](../contracts/git.md#tests) hold the contract suite and the scripted double.

## Alternatives rejected

- Reading repositories through a Go library: range-diff has no library equivalent, and git is already a doctor prerequisite.
- Firstmate's interim helper, which sends a conflict resolution in a touched file to a narrowed review: the slice's tests require a full one.

## Consequences

A broken repository can only widen review, never narrow it.
This is stricter than Firstmate's helper, so some conflict resolutions get a full review that the helper would have narrowed.
