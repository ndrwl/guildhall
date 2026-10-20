---
name: guildhall-contributing
description: Guildhall's contribution rules; use before changing the Guildhall repository and before finishing a change to it.
---

# Contributing to Guildhall

Read these files in the repository before you change it, and follow them wherever this skill is shorter:

- `CONTRIBUTING.md`: the local checks, commit rules and where each record goes.
- `docs/contracts.md`: the contract-first method, which boundaries get an interface, and how tests and doubles are laid out.
- `docs/review.md`: the rules every change is reviewed against, cited by id in findings.
- `docs/decisions.md`: the decisions already made; a change that departs from one records a new decision.

Two rules are easy to miss:

- Write Markdown with one sentence per line.
- Record each engineering decision in its own new file, `docs/decisions/<branch-or-topic-slug>.md`, in the change that makes it, without a number and without editing the `docs/decisions.md` index.
