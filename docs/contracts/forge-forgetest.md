# `internal/forge/forgetest`

## Contract

`RunForgeContract` with its `Harness`, the shared suite for the [forge contract](forge.md): a harness gives a forge, a repository with three heads (two children of the base head and a grandchild, so publishing the third rewrites the branch), the remote, its default branch and base head, a reader of the remote's branches, and optionally a way to make the forge refuse merges.
The cases: a pull request opened for the pushed head and replayed unchanged; the same pull request updated by a new and a rewritten head; an unknown head refused with nothing pushed; the base branch and invalid changes refused with nothing pushed; a merge refused at another head, then merged at the approved head with a merge commit and replayed; and a forge refusal reported as `ErrRefused` with its reason, leaving the pull request open.

`Fake`, the in-memory forge consumers' tests use: commits per repository (`AddCommit`), branches per remote (`SetBranch`, `Branch`, the first one set being the default branch), pull requests (`PullRequests`), a merge refusal (`Refuse`) and call counts (`Calls`).
Its merge moves the default branch to a merge commit derived from the head; it never touches git or the network, and it is safe for concurrent use.
`NewFakeHarness` is the `Fake` under the suite.

## Tests

Run by `internal/forge/forge_test.go` against `Fake` and `forge.GitHub`; `Fake` is the double in `internal/interp/deliver_test.go` and `internal/cli/advance_test.go`.
Both were red first against stubs, as recorded in [`internal/forge`](forge.md#tests).
