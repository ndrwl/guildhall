# D5. Release builds

The release workflow cross-compiles five targets with `CGO_ENABLED=0` and `-trimpath`: linux/amd64, linux/arm64, windows/amd64, darwin/amd64 and darwin/arm64.
Binaries are named `guildhall_<os>_<arch>` (with `.exe` on Windows) and listed in `SHA256SUMS`, providing checksums for the future bootstrap flow (R1.51).
Only a pushed `vMAJOR.MINOR.PATCH[-PRERELEASE]` tag publishes a GitHub release; a manual run builds and checksums only, as a dry run.
The workflow injects the version through `-ldflags -X github.com/ndrwl/guildhall/internal/cli.version`: the tag for tag builds, or `dev-<first 12 commit SHA characters>` for manual branch builds.
Builds without an injected version report the module version Go stamps into the binary, or `dev`.
