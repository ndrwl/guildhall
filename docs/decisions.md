# Engineering decisions

Numbered engineering decisions, recorded by the pull request that makes them.
A later decision may supersede an earlier one by number; entries are not rewritten.
Product requirements live in `docs/requirements.md`.

## D1. One Go module and one binary

The repository is the single Go module `github.com/ndrwl/guildhall` with one binary, `guildhall`, built from `cmd/guildhall`.
Packages live under `internal/` until something outside the module needs them.

## D2. Go 1.27

`go.mod` declares Go 1.27, the current stable release line when the module was created; CI installs the newest 1.27 patch release through `go-version-file`.
The language version moves only by a pull request that updates this entry.

## D3. Standard library only for the foundation

The foundation (`version` and `doctor`) uses only the standard library.
Third-party modules arrive with the first feature that needs them, each recorded here (S7).

## D4. CI matrix and lint

CI runs build, vet, tests, a gofmt check and golangci-lint with its standard linter set on `ubuntu-latest`, `windows-latest` and `macos-latest` for every pull request and every push to `main`.
golangci-lint is pinned to an exact version so lint results do not change underneath a pull request.
`.gitattributes` checks text out with LF endings so formatting checks agree on Windows.

## D5. Release builds

The release workflow cross-compiles five targets with `CGO_ENABLED=0` and `-trimpath`: linux/amd64, linux/arm64, windows/amd64, darwin/amd64 and darwin/arm64.
Binaries are named `guildhall_<os>_<arch>` (with `.exe` on Windows) and listed in `SHA256SUMS`, the checksum file the bootstrap link points at (R1.51).
Only a pushed `vMAJOR.MINOR.PATCH[-PRERELEASE]` tag publishes a GitHub release; a manual run builds and checksums only, as a dry run.
The version is injected with `-ldflags -X github.com/ndrwl/guildhall/internal/cli.version=<tag>`; other builds report the module version Go stamps into the binary, or `dev`.

## D6. `doctor` reports only what it checked

`guildhall doctor` prints the platform, the Guildhall and Go versions, and, for each of `git`, `ssh`, `claude` and `herdr`, either the first line of its version output with its resolved path or the reason it could not be read.
A missing tool is reported, not treated as a failure, so `doctor` exits 0 whenever it could write its report.
Each version command is bounded by a timeout.
