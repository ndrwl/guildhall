# `internal/release/releasetest`

## Contract

`RunBuilderContract` with its `Harness`, the shared suite for the [release `Builder`](release.md): a harness gives a builder, the `release.Probe` that reads a binary it built, its source, a tagged and an untagged `Commit` the source holds, each an `ID` and the `Version` a build of it reports (the tag, or the pseudo-version `v0.0.0-<UTC yyyymmddhhmmss>-<ID[:12]>`), and `Missing`, a full commit id the source lacks.
Each case drives `release.Home.Build` on a fresh temporary home with the harness's builder and probe:

1. the tagged commit builds as its tag, and the installed release records that commit and source and reports that version and revision, unmodified;
2. a pseudo-version of `Missing` is refused as `commit <Missing[:12]> is not in <source>`, with nothing installed, and the builder's own `Resolve` of it, and of a tag the source lacks, is `release.ErrNotInSource`;
3. the untagged commit builds as its pseudo-version, with that revision.

After every build, success or refusal, the home holds nothing but its lock, an empty `.incoming` and, when it succeeded, the release itself, and the source, when it is a local directory, is unchanged, every file and directory with the same mode and content.

`Fixture(t)` makes a temporary git repository holding a Go module (`go 1.24`, at or below the toolchain, so no download is needed) whose `./cmd/guildhall` prints its build info as `version --json`: an untagged commit, then a tagged child, `FixtureTag` (`v1.2.3`), with a fixed identity and commit time, in another zone than UTC, and no signing.
It returns the repository and the two commits; its own git runs drop `GIT_DIR`, `GIT_WORK_TREE` and `GIT_INDEX_FILE` (`GitEnv`), so it is safe to call with them set.
It copies the recipe of `cmd/guildhall/release_build_test.go`, which is `package main`.
`BuildInfoProbe` is the `release.Probe` of a binary go built, reading its version, `vcs.revision` and `vcs.modified` with `debug/buildinfo` rather than running it, with store schema and release contract 1, as the fixture's binary claims.
`AbsentCommit` is a full commit id neither `Fixture` nor `NewFakeHarness` holds, the usual `Missing`.

`Fake`, the `Builder` double consumers' tests use: `Sources` holds the commits of each source, by the source as `Fetch` is given it, and `Resolve` answers against the source last fetched, `refs/tags/<v>` as the commit whose `Version` is `v` and any other revision as the one commit whose id starts with it, else `ErrNotInSource`; `Resolved` answers a revision as given, checked first, to stand in for a builder resolving to a commit the version does not name.
`FetchErr` and `BuildErr` fail every fetch and build; `Steps` records `fetch <source>`, `resolve <rev>`, `checkout <commit>` and `build <pkg>`, `Fetched` the sources fetched and `Built` the builds run.
`Checkout` writes the commit into the work tree, `GoVersion` is `FakeGoVersion` (`go1.27.2`), and `Build` refuses an output inside the work tree and writes a file naming the checked-out commit's version and revision, which `Fake.Probe` reads back, refusing any other file.
It never runs git or go, and is not safe for concurrent use.
`NewFakeHarness` is a `Fake` under the suite, over an empty temporary directory as its source.

## Tests

Run by `internal/release/build_contract_test.go` against `Fake` and `release.ExecBuilder`; `Fake` is the double in `internal/release/build_test.go` and `internal/cli/release_build_test.go`, replacing the two private fakes they had.
The `ExecBuilder` run was written red first in the sense that it had never run before d164; the inherited `GIT_DIR` test beside it was run red against the unscrubbed environment, where the build ran in the other repository.
