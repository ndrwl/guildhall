# Contributing

Keep each pull request small and coherent: one reviewable change with no fixed line cap.
Follow the contract-first method and evidence requirements in `docs/contracts.md`.
Include that test evidence in the pull request description.
Evidence a contract page must cite, such as a post-implementation failure check's captured runs, goes in `docs/evidence/` as files named `<topic>-<date>`, linked from that one page.
Write Markdown with one sentence per line.
Do not add co-author trailers to commit messages.
Pull requests are reviewed against `docs/review.md` and the requirements in `docs/requirements.md`.
Record each engineering decision in its own new file, `docs/decisions/<branch-or-topic-slug>.md`, in the pull request that makes it.
Do not number it or edit the `docs/decisions.md` index: decisions are numbered and indexed by a refresh after merge, so parallel pull requests cannot collide on a number.
The refresh numbers every unnumbered record after each sustainability review, in that review's follow-up batch: in the lane that carries its docs-truth finding, or in the batch's docs lane when the review raised none.
Record a package's contract and tests in its own `docs/contracts/<package>.md`.
Describe a new command in its own `docs/` page and add at most one line for it to the README command list.

Use event-driven synchronization or a synthetic clock in tests; real-process ceilings and justified exceptions follow the [test timing policy](docs/test-timing.md).

## Local checks

CI runs these on Linux, Windows and macOS; run them before opening a pull request.

```sh
gofmt -l .
go build ./...
go vet ./...
go test ./...
golangci-lint run
```

CI also runs these two on Linux only:

```sh
go test -race ./...
python3 .github/tests/test_release.py
```

`go test -race` needs cgo and a C compiler; the release test needs PyYAML 6.0.1 ([D7](docs/decisions/0007-release-policy-regression-test.md)).

`gofmt -l .` must print nothing.
Use the golangci-lint version pinned in `.github/workflows/ci.yml`; the lint policy is recorded in [D4](docs/decisions/0004-ci-matrix-and-lint.md).
Actions in `.github/workflows` are pinned by commit SHA with the tag as a comment ([CI action pins and race](docs/decisions/ci-action-pins-and-race.md)).
