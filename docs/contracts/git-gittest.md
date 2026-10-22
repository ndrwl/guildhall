# `internal/git/gittest`

## Contract

The shared classification contract suite, `RunClassifierContract` with its `Harness` and `Case` histories, and `Script`, a scripted classifier for consumer tests.
`Script` answers what `Add` scripted for a repository and pair of ranges, a full review with a reason for anything else, and an error for an ended context; it records each call and is not safe for concurrent use.
It passes the suite because its harness scripts each case's answer, so it pins the shape of answers, not git's behaviour.

The shared workspace contract suite, `RunWorkspaceContract` with its `WorkspaceHarness`; `Workspaces`, an in-memory double of `git.Program.Ensure` over plain directories that keeps each directory's commits in memory, safe for concurrent use and recording each validated call; and `Repository`, which builds a real bare remote and a clone one commit behind its default branch for tests in other packages.
`Workspaces` passes the suite, so it pins the contract's shape over directories, not git's behaviour: its `Commit` stands for an Artisan's commit.

## Tests

Run by `internal/git/git_test.go`, against `git.Program` and `Script`; `Script` is the double in `internal/interp/rebase_test.go`.
`RunWorkspaceContract` is run by `internal/git/workspace_test.go` against `git.Program` and `Workspaces`, which is the double in `internal/interp/workspace_test.go` and the CLI's run-director tests; `Repository` builds the fixtures of the `git.Program` harness and of the built-binary tests.
The classification suite and `Script` moved here unchanged from `internal/git`'s own tests.
