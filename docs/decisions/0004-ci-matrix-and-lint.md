# D4. CI matrix and lint

CI runs build, vet, tests, a gofmt check and golangci-lint with its standard linter set on `ubuntu-latest`, `windows-latest` and `macos-latest` for every pull request, every push to `main` and manual dispatches for any ref; main runs have independent concurrency groups so later pushes or dispatches neither replace pending runs nor cancel running ones, while other refs share a per-ref group that cancels superseded runs.
golangci-lint is pinned to an exact version so lint results do not change underneath a pull request.
`.gitattributes` checks text out with LF endings so formatting checks agree on Windows.
