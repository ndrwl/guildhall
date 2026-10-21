# `internal/git`

## Contract

The boundary to the git program: each question the Director asks of a repository is answered by running `git --literal-pathspecs -C <repo>` with an argument list, never through a shell.
Its one question so far is `Program.Classify(ctx, repo, last, next)`: whether a task's new commit range `next` is a rebase of the range `last` that was reviewed, for rebase-aware re-review (director slice S6b part 3).

`ScopeRebase` is returned only when all of these hold:

- all four ids are full commit ids, the bases differ, each base is an ancestor of its head and `last.Base` is an ancestor of `next.Base`;
- `git range-diff --no-patch last.Base..last.Head next.Base..next.Head` pairs every commit and marks every pair `=`;
- no file the task changes in `last` was changed between the two bases;
- `next` changes exactly the task's files, each byte-identical to `last.Head`, and every file the base changed is byte-identical at `next.Head` to `next.Base`.

The classification then carries the resolution diff, `git diff next.Base next.Head`, for the reviewer.
Anything else, including an unknown commit, a missing git or any git failure, is `ScopeFull` with a reason and no diff: uncertainty never narrows a review.
`Classify` fails only when its context ends.
It reads objects and writes nothing, so repeating it over the same objects gives the same answer; concurrent calls are safe.
Firstmate's interim `pr-validate` helper is the reference behaviour; unlike it, a base change to any file the task touches is a full review rather than a conflict resolution to review.

Consumers declare their own narrow interface over `Classify`; the interpreter's is follow-up work.
Platform differences: none in the contract; git is found on `PATH` unless `Program.Path` names it.

## Tests

`git_test.go` runs `gittest.RunClassifierContract` against `Program` over histories built with real git in temporary repositories, with global and system configuration excluded, on every CI platform, and against `gittest.Script`.
The cases are an identical-patch rebase (rebase, with a diff), a rebase that also edits a task file, a base change to a file the task touches that leaves the task's patches identical, a new head on the same base and an unknown commit (all full), a repeated call giving the same answer, and an ended context (an error).
The suite was written red first against a stub `Classify` returning zero values: every `program` case failed (`scope = "", want ...` and the ended context succeeding) while the `script` cases passed.
