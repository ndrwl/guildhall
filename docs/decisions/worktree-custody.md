---
status: accepted
date: 2026-10-22
topic: delivery
amended-by: branch-kept.md, task-cancel.md
formerly: D68, 0068-worktree-custody.md
---

# A task's working directory is a git worktree of its repository

## Context

A task's working directory, `<state>/<repo>/<task>/work`, was a plain directory, so a commit an Artisan made there was nowhere [delivery](delivery-stage.md) could push from: `publish` pushes the head from the repository's `path` and refuses a head that is not one of its commits.
This was the first dependency of the Director running a real Guildhall task.
It follows the Guildmaster's git decision for delivery: Guildhall prescribes only the edges (the push target `guildhall/<task>`, the pull request, merging under the repository's rules, never force-pushing the base, never `--admin`), and agents use git freely inside their task copy.

## Decision

The working directory is a git worktree of the repository's configured `path` ([the working directory](../interpreter.md#the-working-directory)), made by [`git.Program.Ensure`](../contracts/git.md#workspaces) through the interpreter's `Workspaces` seam, so the Artisan's commit is a commit of the repository and publish pushes it unchanged.

- **Created by the first attempt, reused by every later one.**
  The engine ensures the worktree before each Artisan launch; reuse never resets, cleans or checks out, so an Artisan's commits, branches and uncommitted files carry over and the Artisan decides recovery.
- **Started at the remote's default branch, on branch `guildhall/<task>`**, fetched straight into that branch so no shared `FETCH_HEAD` is written and concurrent tasks do not race; an existing local branch of that name is used as it is.
  An Artisan may move to another branch: publish pushes the head commit, not the branch.
- **Replay safe.**
  The worktree is built at `<dir>.partial` and moved into place only when complete.
- **An earlier plain working directory is refused** with `ErrNotWorkspace` and the Director decides; an empty directory becomes the worktree.
- **The repository path must be absolute where it runs**: the config accepts either platform's form ([absolute repository paths](repository-paths-are-absolute.md)), and `Ensure`, the layer that opens it, refuses one that is not.
- **Bounded and non-interactive.**
  Each git run is bounded by five minutes and never prompts; credentials stay in git's own configuration.
- **The default branch is recorded for the base.**
  Every `Ensure` also fetches the remote's HEAD into `refs/guildhall/default/<branch>`, one ref per task, and `Program.Base` is the merge base of the task branch and that ref, read without the network; on reuse the fetch is best effort, and an unread or stale base only ever keeps a review full.
- **Retired at merge, never losing work.**
  When the merge closes the task, the engine removes the worktree, its local branch and the default ref only if nothing in them is undelivered, before the task is marked `done` so a crash retires it on replay; otherwise it keeps them and sends the Director a `workspace-kept` message.
  A failed removal is reported the same way and never fails the merge; one cut short by the advance's context fails the advance, so running it again retires the worktree.
  Recorded outputs and ignored files are not work.

## Alternatives rejected

- A clone: it has its own objects, so its commits would not be commits of `path`, and publish would need to fetch from it or push from it, changing the delivery contract.
- Replacing an earlier plain directory: it could lose an open task's files.
- Removing the worktree only by a later Director command, or removal that always keeps the branch: the other two of three cleanup options, decided on 2026-10-22 through Firstmate in favour of engine removal at merge.

## Consequences

Outputs such as `spec.md` and review outcomes still sit in the worktree as untracked files beside the code, so an Artisan that commits should leave them out; moving them out is later work.
An engine with no `Workspaces`, as in the interpreter's own tests, keeps the plain directory.
The base ref was added for the rebase-aware review's production caller (hallmark 11, F38), because the branch fetch wrote no ref of the default branch.
[Branch kept](branch-kept.md) now fast-forwards an untouched branch before every Artisan attempt, and [task cancel](task-cancel.md) closes tasks another way and keeps their worktree and branch for the Director.
