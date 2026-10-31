# `internal/forge/forgetest`

## Contract

`RunForgeContract` with its `Harness`, the shared suite for the [forge contract](forge.md): a harness gives a forge, a repository with three heads (two children of the base head and a grandchild, so publishing the third rewrites the branch), the remote, its default branch and base head, a reader of the remote's branches, and optionally ways to make the forge refuse merges, to set the checks it reports for a pushed head, to read a pull request's title and body (`Text`) and to count the edits of an open one's text (`Edits`).
The cases: a pull request opened for the pushed head and replayed unchanged; the same pull request updated by a new and a rewritten head; a title and body set on a new pull request and rewritten on the open one, with no edit when they are unchanged; a title or body over the forge's limits refused; an unknown head refused with nothing pushed; the base branch and invalid changes refused with nothing pushed; a merge refused at another head, then merged at the approved head with a merge commit and replayed; a forge refusal reported as `ErrRefused` with its reason, leaving the pull request open; no checks reported for a head nothing checked; every check of a head returned sorted by name with its state, URL and run id; a re-run check reported as its new run; the checks of an earlier head still read after a later one is published; an unpushed head refused with `ErrUnknownHead` and a malformed commit with `ErrInvalid`; an open pull request closed, the close replayed and the branch kept, after which a merge is refused; and a merged pull request's close refused with `ErrRefused` and a malformed close with `ErrInvalid`.

`Fake`, the in-memory forge consumers' tests use: commits per repository (`AddCommit`), branches per remote (`SetBranch`, `Branch`, the first one set being the default branch), pull requests (`PullRequests`) with each one's title and body (`Text`) and the count of publishes that changed an open one's text (`Edits`), a merge refusal (`Refuse`), the subject each pull request was merged with (`MergeSubject`), the checks a remote reports for a head (`SetChecks`; a head is held by the remote once a branch was set to it), call counts (`Calls`), and its closes with the comment each posted (`Closes`).
Its merge moves the default branch to a merge commit derived from the head; it never touches git or the network, and it is safe for concurrent use.
`NewFakeHarness` is the `Fake` under the suite.

`Fake.SetMergeability` scripts observations for the consumer's optional mergeability seam and clones path slices on input and output under the fake's mutex.
An empty scripted head resolves to the requested head, and an omitted URL resolves to the published PR's URL.
It performs no network or git operations; production evidence computation is tested separately against real git and strict gh process syntax.

`Fake.CheckLog` implements the optional `interp.CheckLogReader` as `forge.GitHub.CheckLog` does: `SetLog(job, text)` sets a job's log, served through the same `forge.TailWriter` cap, and `SetLogError(job, err)` makes reading it fail with `err` until a nil error clears it; a job with neither has an expired log (`forge.ErrLogExpired`), an empty job or a limit of zero or less is `forge.ErrLogUnavailable` and a cancelled context is returned as it is.
`LogCalls` counts its calls, so consumers can show a log served from a cache made no forge call.

`RunMergeabilityContract` is the shared observation contract for the scripted fake and the strict-gh/real-git fixture.
It checks exact evidence, immutable returned slices, repeated reads, malformed requests and refusal when the requested head no longer matches the observation.

## Tests

`TestFakeCheckLog` in `internal/forge/checklog_test.go` pins the fake's log cap, its failures and its call count.
`TestFakeMergeabilityContract` and `TestGitHubMergeabilityEvidence` run the same `RunMergeabilityContract` suite against both sources.

Run by `internal/forge/forge_test.go` against `Fake` and `forge.GitHub`; `Fake` is the double in `internal/interp/deliver_test.go` and `internal/cli/advance_test.go`.
Both were red first against stubs, as recorded in [`internal/forge`](forge.md#tests).
