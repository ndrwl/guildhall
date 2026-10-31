---
status: accepted
date: 2026-10-29
topic: delivery
amends: worktree-custody.md
---

# Every Artisan attempt starts on the current main, and branch-kept replaces dependency-branch-kept

## Context

Only the [dependency gate](../interpreter.md#the-dependency-gate) fast-forwarded a task branch, and only before the first attempt of a task with `depends_on`.
d23 had no dependencies, so its build retries started on the stale base of its first attempt, and build-3 failed when the builder profile refused the Artisan's own `git merge --ff-only origin/main`.

## Decision

This refines [worktree custody](worktree-custody.md): Guildhall still never resets, cleans or checks out over a task's worktree, but it now moves an untouched branch forward before every Artisan attempt ([the working directory](../interpreter.md#the-working-directory)).

- **One fast-forward point, in launch.**
  The launch path calls `Workspaces.FastForward` after `Ensure` and before the attempt's base is recorded, for every Artisan attempt of a workflow step: a first attempt, a retry, a loop round, a send-back and a relaunch of an attempt never marked launched.
  A build retry therefore starts on the new main and records it as its base for [the head check](../interpreter.md#the-head-check); a relaunch repeats the call, which is idempotent.
  The gate's own two calls are removed and it now only holds or releases; a held task still starts on the main its dependencies merged into, because its first attempt fast-forwards at launch.
  Director stages, as before, and the tiered rebase's synthetic `<step>-rebase` and `<step>-rebase-review` attempts, which move a branch with commits through `Adopt`, are never fast-forwarded.
- **The kept reason is classified with `IsAncestor`.**
  When `FastForward` returns a reason, the engine asks whether the branch tip is an ancestor of `DefaultRef(branch)`: if not, the branch has commits of its own, the normal case after a build's first commit, and nothing is said; if so, an untouched branch was not moved (the fetch failed, the worktree is on another branch or git refused), and that is a **branch kept**.
  `FastForward` and its contract are unchanged.
- **One `branch-kept` message per attempt**, id `<repo>.<task>.branch-kept.<step>-<n>`, saying which attempt runs on which branch and why it was not moved; a replay with another reason gets `ErrMutationConflict`, which is swallowed.
  It covers the dependency case, so `dependency-branch-kept`, whose id named no attempt, is removed from `store.MessageTypes`.
- **The behind-main note says `git rebase`, never `git merge`.**
  A code-head attempt reads `Workspaces.Behind`, the first-parent commits of `DefaultRef(branch)` its tip lacks, and when it is behind or was kept its prompt gets a "Behind main" section: the count, why a kept branch was not moved, and to take main's changes, when needed, with `git fetch` then `git rebase origin/main`.
  Research, plan and review attempts never get it, and it is rendered only with the attempt's first preparation.

## Alternatives rejected

- Parsing the kept reason: it is free text whose wording differs between `git.Program` and the double.
- Telling the Director of every kept branch: after a build's first commit that would be almost every attempt.
- `git merge` for main's changes: a merge commit makes the task branch's history non-linear, so the head check's base and the tiered rebase's replay stop meaning what they should, and the builder profile does not allow it.
- Naming `refs/guildhall/default/<branch>` in the note: it is always right but less familiar, so the note names `origin/main` literally, as the grill decided, and says to fetch first because `Ensure` and `FastForward` refresh that ref, not `origin/main`.

## Consequences

A repository whose remote is not `origin`, or whose default branch is not `main`, gets advice that does not match its names; that is left for a later change.
A `dependency-branch-kept` message still in a mailbox counts as `other` in the wake line and `DirectorUnread`, accepted because the type was rare and sent once per task.
A relaunch that counts differently keeps its prompt as first rendered, and nothing new is recorded in the binding.
The example builder profile grants `git fetch` and `git rebase` so the advice works under it.
`TestUntouchedBranchStartsOnMain` covers a build retry, a send-back to build and the first build after the plan while main moved, each without `depends_on`; with the launch's call removed, as at 37cccaae, every case ran at the old main ([red run](../evidence/branch-kept-2026-10-29-red.txt)).
