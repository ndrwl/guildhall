---
status: accepted
date: 2026-10-31
topic: delivery
tasks: d113, d88
amends: multi-pr-tasks.md
---

# Slices stay sequential: Guildhall does not stack their pull requests until merge-async and GitHub-made heads are designed for

## Context

[Multi-PR tasks](multi-pr-tasks.md) deferred GitHub stacked pull requests to a follow-up that would probe them live on ndrwl/guildhall (d88 Q3).
That follow-up, d113, could not run the probe: no task grant can allow `gh`, so no Artisan can.
Its research wrote a probe script and a hand-run checklist instead, and read GitHub's documentation ([evidence](../evidence/stacked-prs-probe-2026-10.md)).
No probe output exists, and nothing about the live repository's behaviour is confirmed.

## Decision

Guildhall does not stack slice pull requests; slices stay sequential, each cut from main after the slice before it merged.
The documented costs hold whatever the probe would show:

- GitHub's docs say a stack cannot be merged with the legacy synchronous merge endpoints or mutations, and `GitHub.Merge`'s `gh pr merge --squash --match-head-commit` is that mutation; stacking needs `merge-async`, polling and a "requested, not landed" merge state that `run.merge` lacks.
- Starting slice k+1 once slice k passes review means two live slices per task, against the one-slice-at-a-time rule: two worktrees, a branch and review range based on slice k's head, both re-pointed when slice k merges.
- After a partial merge GitHub rebases the upper layers itself, with no switch to turn it off; that head is one Guildhall neither made nor reviewed, so `--match-head-commit`, `Mergeability`'s "moved" check and the reviewed-head rule (R1.36) all refuse it.
- The feature's CLI and API pages still say preview, its token requirements are undocumented, and how stacks meet the up-to-date protection on main, which d23's rebaser would race, is undocumented.
- The gain is small: only the wait between slice k passing review and slice k+1 starting.

Re-open when either holds:

- (a) the Guildmaster runs the probe and its output contradicts the legacy-merge refusal (D4 in the evidence); or
- (b) Guildhall gains a merge-async path for another reason, and the probe then shows that GitHub's rebase after a squash merge leaves the upper layer's own change patch-identical and converges with the merge queue's rebaser (Q6, Q7).

Any future go keeps these constraints: `Change.Base` and a `StackLinker` forge capability, opt-in per repository and off by default; linking through `gh api POST /repos/{o}/{r}/stacks`, with `gh stack link` given PR numbers only as the fallback; never gh-stack's branch, rebase, push or merge commands; and layers merged one at a time from the bottom, each meeting the merge requirements, never "merge all layers beneath".

## Alternatives rejected

- Build stacking now without the probe: it would commit to merge-async, two live slices and adopting GitHub-made heads on undocumented token and protection behaviour.
- Wait for the probe before deciding: no Artisan can run it, and the documented costs stand whatever it shows.
- Use the gh-stack extension for branches and rebases: it pushes, rebases and merges outside Guildhall's reviewed heads and merge requirements.

## Consequences

`forge.Change` has no `Base`, `GitHub.Publish` never passes `--base`, and `GitHub.Merge` stays the synchronous squash merge.
A multi-PR task waits one review-to-start gap per slice.
The probe kit stays in the evidence file, unrun, for whoever re-opens the question.
