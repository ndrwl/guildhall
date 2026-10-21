# Runs the Go checks from the repository root, stopping at the first failure, with module and toolchain downloads off.
# Twin of check.sh: keep the two equivalent.
$ErrorActionPreference = 'Stop'
$env:GOPROXY = 'off'
$env:GOTOOLCHAIN = 'local'
$unformatted = gofmt -l .
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
if ($unformatted) {
    [Console]::Error.WriteLine("gofmt -l . lists unformatted files:`n" + ($unformatted -join "`n"))
    exit 1
}
go vet ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
go test ./...
exit $LASTEXITCODE
