# `internal/git/gittest`

## Contract

The shared classification contract suite, `RunClassifierContract` with its `Harness` and `Case` histories, and `Script`, a scripted classifier for consumer tests.
`Script` answers what `Add` scripted for a repository and pair of ranges, a full review with a reason for anything else, and an error for an ended context; it records each call and is not safe for concurrent use.
It passes the suite because its harness scripts each case's answer, so it pins the shape of answers, not git's behaviour.

The shared workspace contract suite, `RunWorkspaceContract` with its `WorkspaceHarness`, over `Ensure`, `Retire`, `Tip`, `IsAncestor` and `Base`; `Workspaces`, an in-memory double of `git.Program.Ensure`, `Retire`, `Tip`, `IsAncestor` and `Base` over plain directories, where every file is untracked until the double's `Commit`, that keeps each directory's commits in memory, safe for concurrent use and recording each validated call; and `Repository`, which builds a real bare remote and a clone one commit behind its default branch for tests in other packages.
`Repositories(t)` returns a `Repository` for each test it is called with while `t` runs, building the fixture with git only once: each call copies the first fixture's files into its own test's directory and points the copy's `origin` at the copy's remote, so copies are independent of one another and of the first, at one git run each.
`Workspaces` passes the suite, so it pins the contract's shape over directories, not git's behaviour: its `Commit` stands for an Artisan's commit.
Its `Tip` is the branch's commit as `Ensure` and `Commit` left it; its `IsAncestor` walks the parents its commits record and is false for a commit the repository does not have; every `Ensure` records the remote's default branch for the workspace, as `git.Program` records `refs/guildhall/default/<branch>`, and its `Base` is the nearest commit of the branch's history that is also in that default branch's, an error before the first `Ensure` or after `Retire`.

## Tests

Run by `internal/git/git_test.go`, against `git.Program` and `Script`; `Script` is the double in `internal/interp/rebase_test.go`.
`RunWorkspaceContract` is run by `internal/git/workspace_test.go` against `git.Program` and `Workspaces`, which is the double in `internal/interp/workspace_test.go` and the CLI's run-director tests; `Repositories` builds the fixtures of the `git.Program` harness, `TestEnsureProgram`, `TestRetireProgram` and `TestBaseProgram`, and `Repository` those of the built-binary tests.
`workspace_test.go`'s `TestRepositoriesCopiesAreIndependent` was written red first against a `Repositories` returning empty paths ([red run](../evidence/windows-ci-speed-2026-10-22-red.txt)): two copies, each one commit behind its own remote's trunk, with its own `origin`, sharing no directory, and a push through one never reaching the other's remote.
The classification suite and `Script` moved here unchanged from `internal/git`'s own tests.
