# Every Artisan attempt starts on the current main, and branch-kept replaces dependency-branch-kept

Only the [dependency gate](../interpreter.md#the-dependency-gate) fast-forwarded a task branch, and only before the first attempt of a task with `depends_on`.
d23 had no dependencies, so its build retries started on the stale base of its first attempt, and build-3 failed when the builder profile refused the Artisan's own `git merge --ff-only origin/main`.

This refines [D68](0068-worktree-custody.md): Guildhall still never resets, cleans or checks out over a task's worktree, but it now moves an untouched branch forward before every Artisan attempt ([the working directory](../interpreter.md#the-working-directory)).

## One fast-forward point, in launch

The interpreter's launch path calls `Workspaces.FastForward` after `Ensure` and before the attempt's base is recorded, for every Artisan attempt of a workflow step: a first attempt, a retry, a loop round, a send-back and a relaunch of a recorded attempt that was never marked launched.
A build retry therefore starts on the new main and records it as its base, so the [head check](../interpreter.md#the-head-check) measures it from there.
A relaunch repeats the fast-forward, which is harmless because `FastForward` is idempotent.

The gate's own two calls are removed, and the gate now only holds or releases.
A held task still starts on the main its dependencies merged into, because its first research attempt fast-forwards at launch.
A Director stage, such as the grill, is never fast-forwarded, as the gate never did; nor are the tiered rebase's synthetic `<step>-rebase` and `<step>-rebase-review` attempts, which work on a branch with commits and move it with `Adopt`.
A step is a workflow step when it is one of the definition's steps, which the synthetic steps are not.

## Classifying the kept reason with IsAncestor

After a build's first commit, every later attempt's `FastForward` returns a kept reason, because the branch has commits of its own.
That is the normal case, and a message for it would reach the Director on almost every attempt.
The reason is free text whose wording differs between `git.Program` and the double, so the engine does not parse it.
Instead, when a reason is returned, it asks `Workspaces.IsAncestor` whether the branch tip is an ancestor of `DefaultRef(branch)`:

- not an ancestor: the branch has commits of its own, and nothing is said;
- an ancestor: an untouched branch was not moved, because the fetch failed, the worktree is on another branch or git refused, and that is a **branch kept**.

`FastForward` and its contract are unchanged.

## branch-kept retires dependency-branch-kept

A branch kept sends the Director one `branch-kept` message per attempt, id `<repo>.<task>.branch-kept.<step>-<n>`, saying which attempt runs on which branch and why it was not moved.
A replay with a different reason, such as after the Director removed the blocking file, gets `ErrMutationConflict`, which is swallowed, so each attempt sends exactly one.
It covers the dependency case too, so `dependency-branch-kept`, whose id named no attempt and which the gate sent at most once per task, is removed from `store.MessageTypes`.
A `dependency-branch-kept` message still in a mailbox now counts as `other` in the wake line and `DirectorUnread`; that is accepted, since the type was sent rarely and only once per task.

## The behind-main note says git rebase, never git merge

A code-head attempt whose branch has commits of its own is not moved, so it is told how far behind it is instead.
After the fast-forward the engine reads `Workspaces.Behind`, the first-parent commits of `DefaultRef(branch)` the tip lacks, and when the branch is behind or was kept the prompt gets a "Behind main" section: the count, why a kept branch was not moved, and that main's changes are taken, when needed, with `git fetch` then `git rebase origin/main`, never `git merge`.
A merge commit on a task branch makes its history non-linear, so the head check's base and the tiered rebase's replay of the task's own commits stop meaning what they should, and the builder profile does not allow `git merge`.
The example builder profile grants `git fetch` and `git rebase` so the advice works under it.
Research, plan and review attempts never get the note: only a code-head attempt computes it.

The note is rendered only with the attempt's first preparation, since a published prompt is never rendered again, so a relaunch that counts differently keeps the prompt as it was, and nothing new is recorded in the binding.

The note names `origin/main` literally, as the grill decided.
A repository whose remote is not `origin` or whose default branch is not `main` gets advice that does not match its names; `refs/guildhall/default/<branch>`, the ref the count uses, would always be right but is less familiar, and is left for a later change.
`Ensure` and `FastForward` refresh that ref, not `origin/main`, which is why the note says to fetch first.

## Evidence

`TestUntouchedBranchStartsOnMain` covers a build retry, a send-back to build and the first build after the plan while main moved, each with no `depends_on`.
With the launch's fast-forward call removed, which is the behaviour at 37cccaae for a task without dependencies, every case failed: build ran at the old main `aaaa…` and recorded it as its base, where the new main was wanted ([red run](../evidence/branch-kept-2026-10-29-red.txt)).
