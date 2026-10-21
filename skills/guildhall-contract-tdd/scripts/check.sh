#!/bin/sh
# Runs the Go checks from the repository root, stopping at the first failure, with module and toolchain downloads off.
# Twin of check.ps1: keep the two equivalent.
set -eu
export GOPROXY=off GOTOOLCHAIN=local
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
	printf 'gofmt -l . lists unformatted files:\n%s\n' "$unformatted" >&2
	exit 1
fi
go vet ./...
go test ./...
