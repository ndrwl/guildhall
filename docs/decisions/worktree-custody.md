# A task's working directory is a git worktree of its repository

A task's working directory, `<state>/<repo>/<task>/work`, is now a git worktree of the repository's configured `path` ([the working directory](../interpreter.md#the-working-directory)), made by [`git.Program.Ensure`](../contracts/git.md#workspaces) through the interpreter's `Workspaces` seam.
Until now it was a plain directory, so a commit an Artisan made there was nowhere [delivery](0060-delivery-stage.md) could push from: `publish` pushes the head from the repository's `path` and refuses a head that is not one of its commits.
A worktree shares the repository's objects, so the Artisan's commit is a commit of the repository and the existing publish step pushes it unchanged; this is the first dependency of the Director running a real Guildhall task.
It follows the Guildmaster's git decision for delivery: Guildhall prescribes only the edges (the push target `guildhall/<task>`, the pull request, merging under the policy, never force-pushing the base, never `--admin`), and agents use git freely inside their task copy.

The choices the implementation made within that:

- **A worktree, not a clone.** A clone has its own objects, so its commits would not be commits of `path` and publish would need to fetch from it or push from it, changing the delivery contract; a worktree needs neither.
- **Created by the first attempt, reused by every later one.** The engine ensures the worktree before each Artisan launch, so the first attempt of any step creates it and every retry, loop round and relaunch reuses it; reuse never resets, cleans or checks out, so an Artisan's commits, branches and uncommitted files carry over and the Artisan decides recovery.
- **Started at the remote's default branch, on branch `guildhall/<task>`.** A new worktree's local branch is the branch publish pushes to, created at the commit the remote's HEAD is at, fetched straight into that branch so no shared `FETCH_HEAD` is written and concurrent tasks do not race; a local branch of that name that already exists is used as it is, so a worktree created again keeps its commits.
  An Artisan may move to another branch: publish pushes the head commit, not the branch.
- **Replay safe by building beside it.** The worktree is added at `<dir>.partial` and moved into place only when complete, so a crash leaves no working directory or a complete one, and the next advance clears the leftover and launches the attempt it recorded.
- **An earlier plain working directory is refused, not replaced.** An open task started before this change has outputs in a plain `work/`; replacing it could lose files, so `Ensure` refuses it with `ErrNotWorkspace` and the Director decides; an empty directory becomes the worktree.
- **The repository path must be absolute on the Director's machine.** The config accepts an owner-machine path in either platform's form ([D11](0011-repository-paths-are-absolute-in-the.md)); `Ensure` is the layer that opens it, and refuses one that is not absolute where it runs.
- **Bounded and non-interactive.** Each git run is bounded by five minutes, as the forge's are, and never prompts on the terminal; credentials stay in git's own configuration.
- **The default branch is recorded for the base.** Added for the rebase-aware review's production caller (hallmark 11, F38): the branch fetch writes no ref of the default branch, so a task's base had nothing local to be read from.
  Every `Ensure` now also fetches the remote's HEAD into `refs/guildhall/default/<branch>`, one ref per task so concurrent tasks do not race, and `Program.Base` is the merge base of the task branch and that ref, read without the network.
  On reuse that fetch is best effort, so an offline or unreachable remote still reuses the worktree and leaves the ref, and with it the base, as it was; an unread or stale base only ever keeps a review full.
  `Retire` deletes the ref with the branch.
- **Retired at merge, never losing work.** The cleanup was chosen on 2026-10-22, through Firstmate, from three options (engine removal at merge, a later Director command only, or removal that always keeps the branch): when the merge closes the task, the engine removes the worktree and its local branch only if nothing in them is undelivered, and otherwise keeps both and sends the Director a `workspace-kept` message saying what they hold; a removal that fails is reported the same way and never fails the merge.
  A removal cut short by the advance's context ending is the exception: it fails the advance before the task closes, so running it again retires the worktree.
  It runs before the task is marked `done`, because a closed task is never advanced again, so a crash before closing retires it on replay.
  A recorded output in the worktree is not work, since Guildhall already holds its bytes; ignored files are not work either.
  Tasks that close any other way keep their worktree; nothing cancels a task yet.

Outputs such as `spec.md` and review outcomes are still written into the working directory, so they now sit as untracked files beside the code; an Artisan that commits should leave them out, and moving them out of the worktree is later work.
An engine with no `Workspaces`, as in the interpreter's own tests, keeps the plain directory.
