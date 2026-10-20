# D4. CI matrix and lint

CI runs build, vet, tests, a gofmt check and golangci-lint with its standard linter set on `ubuntu-latest`, `windows-latest` and `macos-latest` for every pull request and every push to `main`.
golangci-lint is pinned to an exact version so lint results do not change underneath a pull request.
`.gitattributes` checks text out with LF endings so formatting checks agree on Windows.
