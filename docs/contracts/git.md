# `internal/git`

## Contract

The boundary to the git program: each question the Director asks of a repository is answered by running git with an argument list, never through a shell.
It answers two: `Program.Ensure`, which makes a task's working directory a worktree of its repository ([workspaces](#workspaces)), and `Program.Classify(ctx, repo, last, next)`, run as `git --literal-pathspecs -C <repo>`: whether a task's new commit range `next` is a rebase of the range `last` that was reviewed, for rebase-aware re-review (director slice S6b part 3).

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

### Workspaces

`Program.Ensure(ctx, w)` makes `w.Dir` a git worktree of the repository at `w.Repository`, so that a commit made there is a commit of the repository that `forge.GitHub.Publish` pushes ([the working directory](../interpreter.md#the-working-directory), [decision](../decisions/worktree-custody.md)).

- **Create.** A missing or empty `w.Dir` becomes a worktree on branch `w.Branch`: an existing local branch of that name as it is, otherwise a new one at the commit `w.Remote`'s HEAD, its default branch, is at now, fetched with `git fetch --no-tags --no-write-fetch-head <remote> HEAD:refs/heads/<branch>`, which writes no shared `FETCH_HEAD`.
- **Reuse.** A `w.Dir` that is the top of a worktree of the same repository is reused unchanged, on whatever branch or commit it is at and with whatever it holds; only `git worktree repair` runs, which changes nothing unless git's records of the worktree are stale.
- **Refuse.** A `w.Dir` that exists, is not empty and is not such a worktree, including a plain directory, a checkout of another repository and a subdirectory of a checkout, is `ErrNotWorkspace`, changing nothing; a workspace with a relative path, no remote, or a branch `git check-ref-format` refuses or that starts with `-` is `ErrInvalidWorkspace`.
- **Replay.** The worktree is added at `<dir>.partial` and moved into place with `git worktree move` only when complete, so an interrupted `Ensure` leaves no `w.Dir` or a complete one; the next `Ensure` removes any `<dir>.partial`, registered or not, since nothing ran in it, and finishes.
  A branch an interrupted `Ensure` already created is used as it is.
- **Errors.** A git failure, such as an unreachable remote or a missing repository, is an error naming git's command and reason, with nothing created; each git run is bounded by `Program.Timeout`, default five minutes, and never prompts on the terminal; an ended context is its error.
- **Concurrency.** Calls for one workspace must be serialised, as the task lock does; calls for different workspaces of one repository are safe, since each has its own directory and branch and git locks its own records.

Directories are compared by file identity, not by path text, so symbolic links such as macOS's `/var`, letter case and Windows short names do not matter.
`w.Repository` and `w.Dir` must be absolute in the form of the machine `Ensure` runs on: an owner-machine path in another platform's form, which [the config accepts](../decisions/0011-repository-paths-are-absolute-in-the.md), is `ErrInvalidWorkspace` there.
Paths with spaces are passed as single arguments, never through a shell.

## Tests

[`internal/git/gittest`](git-gittest.md) defines `RunClassifierContract` and the scripted double `Script`, shared with the interpreter's tests.
`git_test.go` runs the suite against `Program` over histories built with real git in temporary repositories, with global and system configuration excluded during history setup, on every CI platform, and against `Script`.
The cases are an identical-patch rebase (rebase, with a diff), a rebase that also edits a task file, a base change to a file the task touches that leaves the task's patches identical, a new head on the same base and an unknown commit (all full), a repeated call giving the same answer, and an ended context (an error).
Submodule histories with both diff-wide and per-submodule ignore settings enabled cover a valid rebase whose reviewer diff includes the pointer and a base/task gitlink overlap requiring full review.
`gittest.RunWorkspaceContract` runs against `Program`, over a bare remote whose default branch `trunk` has moved past what the repository fetched (`gittest.Repository`), and against the double `gittest.Workspaces`: creation at the remote's default branch, reuse across attempts keeping a commit and an uncommitted file with the commit a commit of the repository, an empty directory, a refused plain directory left untouched, a directory with spaces, two independent workspaces of one repository, invalid workspaces creating nothing and an ended context creating nothing.
`workspace_test.go`'s `TestEnsureProgram` covers what only git shows: a registered and an unregistered leftover `<dir>.partial` finished, a removed worktree created again on its branch's commits, an agent's own branch kept on reuse, another repository's worktree and subdirectories of checkouts refused, an unreachable remote creating nothing, and the edge publish needs, the worktree's commit found in and pushed from the repository exactly as `forge.GitHub.Publish` pushes it.
Both were written red first against a stub `Ensure` returning nil ([red run](../evidence/worktree-custody-2026-10-22-red.txt)): every `program` case failed.
The classification suite was written red first against a stub `Classify` returning zero values: every `program` case failed (`scope = "", want ...` and the ended context succeeding) while the `script` cases passed.
