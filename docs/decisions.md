# Engineering decisions

Numbered engineering decisions, recorded by the pull request that makes them.
A later decision may supersede an earlier one by number; entries are not rewritten.
Product requirements live in `docs/requirements.md`.

## D1. One Go module and one binary

The repository is the single Go module `github.com/ndrwl/guildhall` with one binary, `guildhall`, built from `cmd/guildhall`.
Packages live under `internal/` until something outside the module needs them.

## D2. Go 1.27

`go.mod` declares Go 1.27, the current stable release line when the module was created; CI installs the newest 1.27 patch release through `go-version-file`.
The language version moves only by a pull request that records a superseding decision.

## D3. Standard library only for the foundation

The foundation (`version` and `doctor`) uses only the standard library.
Third-party modules arrive with the first feature that needs them, each recorded here (S7).

## D4. CI matrix and lint

CI runs build, vet, tests, a gofmt check and golangci-lint with its standard linter set on `ubuntu-latest`, `windows-latest` and `macos-latest` for every pull request and every push to `main`.
golangci-lint is pinned to an exact version so lint results do not change underneath a pull request.
`.gitattributes` checks text out with LF endings so formatting checks agree on Windows.

## D5. Release builds

The release workflow cross-compiles five targets with `CGO_ENABLED=0` and `-trimpath`: linux/amd64, linux/arm64, windows/amd64, darwin/amd64 and darwin/arm64.
Binaries are named `guildhall_<os>_<arch>` (with `.exe` on Windows) and listed in `SHA256SUMS`, providing checksums for the future bootstrap flow (R1.51).
Only a pushed `vMAJOR.MINOR.PATCH[-PRERELEASE]` tag publishes a GitHub release; a manual run builds and checksums only, as a dry run.
The workflow injects the version through `-ldflags -X github.com/ndrwl/guildhall/internal/cli.version`: the tag for tag builds, or `dev-<first 12 commit SHA characters>` for manual branch builds.
Builds without an injected version report the module version Go stamps into the binary, or `dev`.

## D6. `doctor` reports only what it checked

The user-facing `guildhall doctor` report is described in the README.
A missing tool is reported, not treated as a failure, so `doctor` exits 0 whenever it could write its report.
Each version command is bounded by a timeout.

## D7. Release policy regression test

The focused release policy test uses Python 3 and PyYAML 6.0.1 to parse the workflow into a semantic model rather than matching YAML source text.
Run it with `python3 .github/tests/test_release.py` in an environment with PyYAML available.
It evaluates the publication condition's conjunction of equality predicates for pushed tags, manual tags, manual branches and pushed branches, and checks that manual builds retain their artifact-upload path.
These are development-only test dependencies; the binary remains standard-library-only.

## D8. TOML parsing for copy configuration

Copy configuration uses TOML (R1.11), which the Go standard library does not parse.
Add `github.com/BurntSushi/toml` v1.6.0 for `internal/config`, following D3's policy of adding a dependency when a feature needs it.
Its typed decoding and undecoded-key metadata let the configuration parser populate concrete Go types and reject unknown keys without maintaining a custom TOML parser.
Workflow JSON continues to use the standard library's `encoding/json`; this dependency adds no execution or activation behavior.
