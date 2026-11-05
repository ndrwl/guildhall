#!/usr/bin/env bash
# Installs the pinned golangci-lint release binary for CI's Lint step
# (docs/decisions/ci-matrix-and-lint.md).
#
#   install-golangci-lint.sh <version> <dir>
#
# downloads the release asset for this runner's OS and arch (.tar.gz on Linux
# and macOS, .zip on Windows) and the release's checksums file, retrying
# transient failures such as a release-CDN 504, fails unless the asset's
# sha256 matches the published one, and unpacks golangci-lint into <dir>.
#
# GOLANGCI_LINT_BASE_URL overrides the release download URL, for testing the
# checksum check against local files (file://...).
set -euo pipefail

if [ $# -ne 2 ]; then
	echo "usage: $0 <version, such as v2.15.0> <dir>" >&2
	exit 2
fi
version=$1
dir=$2
ver=${version#v}
base=${GOLANGCI_LINT_BASE_URL:-https://github.com/golangci/golangci-lint/releases/download/$version}

case "$(uname -s)" in
Linux) os=linux ext=tar.gz ;;
Darwin) os=darwin ext=tar.gz ;;
MINGW* | MSYS* | CYGWIN*) os=windows ext=zip ;;
*)
	echo "unsupported OS $(uname -s)" >&2
	exit 1
	;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*)
	echo "unsupported arch $(uname -m)" >&2
	exit 1
	;;
esac

name=golangci-lint-$ver-$os-$arch
asset=$name.$ext
sums=golangci-lint-$ver-checksums.txt
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fetch() {
	echo "downloading $base/$1"
	curl --fail --silent --show-error --location --retry 5 --retry-all-errors --retry-delay 5 -o "$work/$1" "$base/$1"
}
fetch "$sums"
fetch "$asset"

want=$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$work/$sums")
if [ -z "$want" ]; then
	echo "$sums lists no checksum for $asset" >&2
	exit 1
fi
if command -v sha256sum >/dev/null; then
	got=$(sha256sum "$work/$asset" | awk '{ print $1 }')
else
	got=$(shasum -a 256 "$work/$asset" | awk '{ print $1 }')
fi
if [ "$got" != "$want" ]; then
	echo "checksum mismatch for $asset: got $got, $sums lists $want" >&2
	exit 1
fi
echo "$asset sha256 $got matches $sums"

case "$ext" in
tar.gz) tar -xzf "$work/$asset" -C "$work" ;;
zip)
	if command -v unzip >/dev/null; then
		unzip -q "$work/$asset" -d "$work"
	else
		# 7z is a native Windows program, so it takes Windows paths.
		7z x -bd -y -o"$(cygpath -w "$work")" "$(cygpath -w "$work/$asset")" >/dev/null
	fi
	;;
esac
bin=golangci-lint
[ "$os" = windows ] && bin=golangci-lint.exe
mkdir -p "$dir"
mv "$work/$name/$bin" "$dir/$bin"
"$dir/$bin" --version
