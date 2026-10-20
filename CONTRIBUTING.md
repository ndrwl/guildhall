# Contributing

Keep each pull request small and coherent: one reviewable change with no fixed line cap.
Follow test-driven development: write a failing test first, then make it pass.
Show the red-then-green test evidence in the pull request description.
Write Markdown with one sentence per line.
Do not add co-author trailers to commit messages.
Pull requests are reviewed against `docs/review.md` and the requirements in `docs/requirements.md`.
Record engineering decisions in `docs/decisions.md` in the pull request that makes them.

## Local checks

CI runs these on Linux, Windows and macOS; run them before opening a pull request.

```sh
gofmt -l .
go build ./...
go vet ./...
go test ./...
golangci-lint run
```

`gofmt -l .` must print nothing.
Use the golangci-lint version pinned in `.github/workflows/ci.yml`; the lint policy is recorded in `docs/decisions.md`.
