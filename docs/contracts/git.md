# `internal/git`

## Contract

The boundary to the git program: each question the Director asks of a repository is answered by running git with an argument list, never through a shell.
It answers `Program.FinalHead`, the committed output of a task's work ([final head](#final-head)), and `Program.Ensure`, which makes a task's working directory a worktree of its repository ([workspaces](#workspaces)), `Program.Base`, a task's base ([workspaces](#workspaces)), and `Program.Classify(ctx, repo, last, next)`, run as `git -C <repo> --literal-pathspecs`: whether a task's new commit range `next` is a rebase of the range `last` that was reviewed, for rebase-aware re-review (director slice S6b part 3).

`ScopeRebase` is returned only when all of these hold:

- all four ids are full commit ids, the bases differ, each base is an ancestor of its head and `last.Base` is an ancestor of `next.Base`;
- `git range-diff --no-patch last.Base..last.Head next.Base..next.Head` pairs every commit and marks every pair `=`;
- no file the task changes in `last` was changed between the two bases;
- `next` changes exactly the task's files, each byte-identical to `last.Head`, and every file the base changed is byte-identical at `next.Head` to `next.Base`.

The classification then carries the resolution diff, `git diff next.Base next.Head`, for the reviewer.
Every diff and range-diff uses `--ignore-submodules=none`, overriding both `diff.ignoreSubmodules` and per-submodule ignore settings so gitlink changes remain visible.
Anything else, including an unknown commit, a missing git or any git failure, is `ScopeFull` with a reason and no diff: uncertainty never narrows a review.
`Classify` fails only when its context ends.
It reads objects and writes nothing, so repeating it over the same objects gives the same answer; concurrent calls are safe.
Firstmate's interim `pr-validate` helper is the reference behaviour; unlike it, a base change to any file the task touches is a full review rather than a conflict resolution to review.

Consumers declare their own narrow interface over `Classify`; the interpreter's is `interp.Rebases`.
Platform differences: none in the contract; git is found on `PATH` unless `Program.Path` names it.

### Final head

`Program.FinalHead(ctx, dir)` returns the full commit object id of `HEAD^{commit}` in the working directory, without a newline.
Staged or unstaged tracked changes, including submodules, refuse it with an error naming the dirty paths; untracked files alone do not block it.
Missing Git, a missing repository, an unborn HEAD, cancellation and other Git failures are errors, never fabricated outputs.
It uses the bounded Git runner and literal argument lists on every platform.
The caller must hold exclusive custody while reading; it does not lock out external Git writers.

### Workspaces

`Program.Ensure(ctx, w)` makes `w.Dir` a git worktree of the repository at `w.Repository`, so that a commit made there is a commit of the repository that `forge.GitHub.Publish` pushes ([the working directory](../interpreter.md#the-working-directory), [decision](../decisions/0068-worktree-custody.md)).

- **Create.** A missing or empty `w.Dir` becomes a worktree on branch `w.Branch`: an existing local branch of that name as it is, otherwise a new one at the commit `w.Remote`'s HEAD, its default branch, is at now, fetched with `git fetch --no-tags --no-write-fetch-head <remote> HEAD:refs/heads/<branch>`, which writes no shared `FETCH_HEAD`.
- **Reuse.** A `w.Dir` that is the top of a worktree of the same repository is reused unchanged, on whatever branch or commit it is at and with whatever it holds; only `git worktree repair` runs, which changes nothing unless git's records of the worktree are stale, and the default branch is recorded again.
- **The default branch.** Every `Ensure` records the commit `w.Remote`'s HEAD is at as `refs/guildhall/default/<branch>` (`DefaultRef`) in the repository: a new branch's fetch writes both refs, and otherwise `git fetch --no-tags --no-write-fetch-head <remote> +HEAD:refs/guildhall/default/<branch>` runs on its own, where a failure, such as an unreachable remote, is not an error and leaves the ref as it was, so a reused worktree needs no remote; an ended context is still its error.
- **Refuse.** A `w.Dir` that exists, is not empty and is not such a worktree, including a plain directory, a checkout of another repository and a subdirectory of a checkout, is `ErrNotWorkspace`, changing nothing; a workspace with a relative path, no remote, or a branch `git check-ref-format` refuses or that starts with `-` is `ErrInvalidWorkspace`.
- **Replay.** The worktree is added at `<dir>.partial` and moved into place with `git worktree move` only when complete, so an interrupted `Ensure` leaves no `w.Dir` or a complete one; the next `Ensure` removes any `<dir>.partial`, registered or not, since nothing ran in it, and finishes.
  A missing `w.Dir` git still registers, such as a worktree directory deleted without `git worktree remove`, has only that stale registration removed, so the branch can be checked out again.
  A branch an interrupted `Ensure` already created is used as it is.
- **Errors.** A git failure, such as an unreachable remote or a missing repository, is an error naming git's command and reason, with nothing created; each git run is bounded by `Program.Timeout`, default five minutes, and never prompts on the terminal; an ended context is its error.
- **Concurrency.** Calls for one workspace must be serialised, as the task lock does; calls for different workspaces of one repository are safe, since each has its own directory and branch and git locks its own records.

`Program.Base(ctx, repository, branch)` is the task's base: `git merge-base refs/heads/<branch> refs/guildhall/default/<branch>`, the commit where the task branch left the default branch as `Ensure` last recorded it, read with no network call, so it moves when the branch is rebased onto a newer default branch the workspace fetched.
An invalid branch (`ErrInvalidWorkspace`), a branch or recorded default branch that does not exist, such as before the first `Ensure` or after `Retire`, and any git failure are errors.

`Program.Tip(ctx, repository, branch)` is the commit `refs/heads/<branch>` is at, `git show-ref --verify --hash`, as `Retire` reads it; an invalid branch (`ErrInvalidWorkspace`), a branch that does not exist and any git failure are errors.
`Program.IsAncestor(ctx, repository, commit, head)` reports whether `commit` is `head` or one of its ancestors, `git merge-base --is-ancestor`, whose exit code 1 is false.
A `commit` or `head` the repository does not have, found by `git rev-parse --verify --quiet <id>^{commit}` exiting 1, is false, not an error; any other git failure and an ended context are errors.
The interpreter's [head check](../interpreter.md#the-head-check) reads the task branch's tip, its base and their ancestry through these three.

### FastForward

`Program.FastForward(ctx, w) (kept string, err error)` moves an untouched task branch to the commit the remote's default branch is at now, so a task the [dependency gate](../interpreter.md#the-dependency-gate) held starts from the main its dependencies merged into ([the working directory](../interpreter.md#the-working-directory)).
It is beside `Ensure` and `Retire`, whose contracts it does not change.

- **No worktree.** A `w.Dir` that is not a worktree of `w.Repository`, including a missing one, is left alone and nothing is returned: a later `Ensure` creates the branch at the current main anyway.
- **The default branch.** It fetches `w.Remote`'s HEAD as `Ensure` does and records it as `DefaultRef(w.Branch)`, so `Base` and rebase-aware review stay current.
- **Move.** When the worktree is on `w.Branch` and its HEAD is an ancestor of that commit, it runs `git merge --ff-only` in the worktree. That only fast-forwards and never rebases; untracked files, such as a Director stage's outputs, are kept, and tracked changes that do not conflict are carried along.
- **Keep.** When the remote cannot be fetched, the worktree is on another branch, the branch has commits that are not on the default branch, or git refuses the fast-forward, for example because an untracked file would be overwritten, nothing changes and `kept` says why; that is not an error.
- **Replay.** It is idempotent: a branch already at or ahead of the default branch is left as it is and nothing is returned.
- **Errors.** An invalid workspace is `ErrInvalidWorkspace`; another git failure is an error naming git's reason, and an ended context is its error.

The double `gittest.Workspaces` implements it over its own directories: `AdvanceDefault(remote, name)` adds a commit adding the file `name` to the remote's default branch, `FastForward` refuses a move over a file one of those commits adds that already exists in the directory, as git refuses to overwrite an untracked file, and `FastForwards` lists the calls.

### Retire

`Program.Retire(ctx, r)` removes a merged task's worktree and local branch when nothing in them would be lost, and otherwise removes nothing and returns why it kept them ([the working directory](../interpreter.md#the-working-directory)).

- **What is work.** A changed or staged tracked file (`git status --porcelain -z --untracked-files=all`); an untracked file that is not one of `r.Outputs`, the outputs Guildhall recorded, with the same sha256; a worktree HEAD or branch tip that is not `r.Head` or its ancestor (`git merge-base --is-ancestor`).
  Ignored files are not work and go with the worktree; other local branches and stashes are the repository's and stay.
- **Removal.** `git worktree remove --force`, since recorded outputs and ignored files remain, then `git update-ref -d refs/heads/<branch> <tip>`, so the branch is deleted only while it is still at the tip that was checked, then the recorded default branch, `refs/guildhall/default/<branch>`, which is also deleted from a workspace already retired.
- **Replay.** A missing `w.Dir` with no branch is already retired, whatever the head; a branch left by an interrupted retirement is deleted when its tip is merged.
- **Errors.** An invalid head or workspace is `ErrInvalidWorkspace`; a `w.Dir` that exists and is not a worktree of the repository is `ErrNotWorkspace`; a head the repository lacks and any git failure are errors; nothing is removed on any of them.

Directories are compared by file identity, not by path text, so symbolic links such as macOS's `/var`, letter case and Windows short names do not matter.
`w.Repository` and `w.Dir` must be absolute in the form of the machine `Ensure` runs on: an owner-machine path in another platform's form, which [the config accepts](../decisions/0011-repository-paths-are-absolute-in-the.md), is `ErrInvalidWorkspace` there.
Paths with spaces are passed as single arguments, never through a shell.

Every git run of `Classify`, `Ensure`, `FastForward`, `Base`, `Tip`, `IsAncestor`, `FinalHead`, `Retire`, `Subject`, `FetchDefault` and `Behind` goes through one runner: an argument list under the caller's context, bounded by `Program.Timeout` (`DefaultTimeout`, five minutes, when zero), its pipes closed five seconds after the bound (`WaitDelay`), with `GIT_TERMINAL_PROMPT=0` so git never prompts.
A run past its bound is ended and is a plain error naming git's command and the bound; for `Classify` that is a full scope with that reason, since its context did not end.
`forge.GitHub` keeps the same rule for `gh` and `git` ([forge](forge.md)); the shared runner both would use waits for a third consumer that runs a program under a bound, and is done when `exec.CommandContext` in `internal/forge`, `internal/git` and `internal/herdr` is found only in it.

### Version reads

[`guildhall version`](cli.md#version) reads Guildhall's own repository through three operations:

- `Program.Subject(ctx, repo, rev)` is the subject line of the full commit id `rev` (`git log -1 --format=%s <rev>^{commit}`); an invalid id, a commit the repository lacks and any git failure are errors.
- `Program.FetchDefault(ctx, repo, remote, ref)` records the commit `remote`'s default branch is at as `ref` with `git fetch --no-tags --no-write-fetch-head --no-recurse-submodules <remote> +HEAD:<ref>`; `ref` must be under `refs/guildhall/` and `remote` must not be empty or start with `-`, and an unreachable remote, a run past the bound and any git failure are errors that leave `ref` as it was.
- `Program.Behind(ctx, repo, rev, ref)` is how many first-parent commits `ref` has after `rev` and their subjects, newest first (`git rev-list --count --first-parent` and `git log --first-parent --format=%s` of `rev..ref`), read with no network call; a missing ref, an unknown commit and any git failure are errors.

The CLI declares its own narrow interface over them, `cli.VersionGit`, and bounds each run at five seconds.

## Tests

`history_test.go`'s `TestVersionReads` covers the version reads over `gittest.Repository`: a subject, an unknown commit and a name that is not a full id refused, `Behind` before any fetch failing, a fetch that names two new commits with and without a pull request number, none behind at the tip, and a fetch from a missing remote failing with the ref unmoved and invalid refs and remotes refused.
It was written with the operations and passed on its first run.

`head_test.go` adds green-first boundary coverage for `FinalHead`: committed and detached HEAD, untracked notes, modified/staged/deleted/renamed/added tracked paths, unborn HEAD, non-repository directories, missing executable, cancellation and process timeout.
The red-first integration regression that drove this new operation is linked from the [interpreter contract](interp.md).

[`internal/git/gittest`](git-gittest.md) defines `RunClassifierContract` and the scripted double `Script`, shared with the interpreter's tests.
`git_test.go` runs the suite against `Program` over histories built with real git in temporary repositories, with global and system configuration excluded during history setup, on every CI platform, and against `Script`.
The cases are an identical-patch rebase (rebase, with a diff), a rebase that also edits a task file, a base change to a file the task touches that leaves the task's patches identical, a new head on the same base and an unknown commit (all full), a repeated call giving the same answer, and an ended context (an error).
`TestClassifyTimeout`, red first against the classifier's own runner without a bound ([red run](../evidence/h11-one-runner-2026-10-22-red.txt)), runs this test binary as a git that never answers under a 200 ms `Timeout`: the classification is full with a reason naming `merge-base` and the bound, and `Classify` does not fail.
Submodule histories with both diff-wide and per-submodule ignore settings enabled cover a valid rebase whose reviewer diff includes the pointer and a base/task gitlink overlap requiring full review.
`gittest.RunWorkspaceContract` runs against `Program`, over a bare remote whose default branch `trunk` has moved past what the repository fetched (`gittest.Repository`), and against the double `gittest.Workspaces`: creation at the remote's default branch, reuse across attempts keeping a commit and an uncommitted file with the commit a commit of the repository, an empty directory, a refused plain directory left untouched, a directory with spaces, two independent workspaces of one repository, invalid workspaces creating nothing and an ended context creating nothing.
`workspace_test.go`'s `TestEnsureProgram` covers what only git shows: a registered and an unregistered leftover `<dir>.partial` finished, a removed worktree and one deleted without git (the latter case added with its fix during review, with no recorded red run) each created again on its branch's commits, an agent's own branch kept on reuse, another repository's worktree and subdirectories of checkouts refused, an unreachable remote creating nothing, and the edge publish needs, the worktree's commit found in and pushed from the repository exactly as `forge.GitHub.Publish` pushes it.
The suite's fast-forward cases: an untouched branch moved to a default branch that moved, keeping an untracked output, and moved again to nothing; a missing worktree left missing; a diverged branch and a branch an untracked file blocks each kept with a reason; an invalid workspace refused.
`TestFastForwardProgram` covers `DefaultRef` and `Base` moving with the fast-forward, a tracked change carried along, an ended context and a worktree on another branch kept.
The suite's tip and ancestry cases: `Tip` and `Base` of a branch before `Ensure` are errors; after it the tip is the default branch's commit, and after a commit the tip is that commit while the base stays; `IsAncestor` is true for the base of a commit and a commit of itself, false for a commit of its parent and for an unknown commit on either side, and an ended context is an error for it and `Tip`.
The suite's retirement cases: a merged workspace retired, twice; a workspace never created already retired; an untracked file that is not an output, an output with other bytes and a commit after the merged head each keeping the workspace with a reason; an invalid head or directory and a plain directory refused.
`TestRetireProgram` covers the worktree and its branch removed with an ignored file present and another branch kept, a worktree behind the merged head retired, a changed tracked file and an unmerged branch tip each kept, an interrupted retirement deleting the left branch, and a head the repository lacks failing with nothing removed.
They were written red first against a stub `Retire` returning nothing ([red run](../evidence/worktree-custody-2026-10-22-red.txt)): every `program` case failed except a workspace never created and a worktree behind the merged head, which the stub satisfied by removing nothing where nothing existed; the latter was then given its removal assertions, which the implementation passes.
`base_test.go`'s `TestBaseProgram`, red first against a stub `Base` returning an error ([red run](../evidence/h11-rebase-base-2026-10-22-red.txt)), covers a new workspace based on the default branch it started at, the base unmoved by the remote moving and moved by a rebase onto the default branch a reused workspace fetched, read with the remote gone, a reused workspace with an unreachable remote still reused, and no base before `Ensure` or after `Retire`, which leaves no `refs/guildhall/` ref, nor for an invalid branch; the last two cases passed against the stub, which failed every call.
`Ensure`'s cases were written red first against a stub `Ensure` returning nil ([red run](../evidence/worktree-custody-2026-10-22-red.txt)): every `program` case failed.
The classification suite was written red first against a stub `Classify` returning zero values: every `program` case failed (`scope = "", want ...` and the ended context succeeding) while the `script` cases passed.
