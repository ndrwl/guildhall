#!/usr/bin/env bash
# Platform-neutral packages for CI (docs/decisions/ci-neutral-packages-ubuntu-only.md).
#
#   neutral-packages.sh [neutral]  print the import paths of the module's
#                                  platform-neutral packages, sorted
#   neutral-packages.sh tested     print every other package of go list ./...,
#                                  sorted: the ones Windows and macOS test
#
# A package is neutral when none of its own .go files, _test.go included, has a
# //go:build or // +build line, has a GOOS or GOARCH filename suffix, or
# references runtime.GOOS. Imported packages are not considered.
#
# Runs in Git Bash and Apple's bash 3.2: no mapfile, no associative arrays.
set -euo pipefail

usage() {
	echo "usage: $0 [neutral | tested]" >&2
	exit 2
}

# osarch prints every GOOS and GOARCH value go knows, as one alternation.
osarch() {
	go tool dist list | tr '/' '\n' | sort -u | paste -s -d '|' -
}

# specific succeeds when the package in directory $1 has an OS-specific file.
# $2 is the osarch alternation.
specific() {
	local dir=$1 suffix="_(${2})(_test)?\$" file base
	for file in "$dir"/*.go; do
		[ -e "$file" ] || continue
		base=${file##*/}
		base=${base%%.*}
		# Go reads the suffix from the first underscore on, so "linux.go"
		# and "linux_test.go" are not constrained but "x_linux.go",
		# "x_linux_amd64.go" and "x_linux_test.go" are.
		case "$base" in
		*_*)
			base=_${base#*_}
			if [[ $base =~ $suffix ]]; then
				return 0
			fi
			;;
		esac
		if grep -Eq '^//go:build|^// \+build|runtime\.GOOS' "$file"; then
			return 0
		fi
	done
	return 1
}

mode=${1:-neutral}
[ $# -le 1 ] || usage
case "$mode" in
neutral | tested) ;;
*) usage ;;
esac

tokens=$(osarch)
if [ -z "$tokens" ]; then
	echo "neutral packages: go tool dist list printed nothing" >&2
	exit 1
fi
list=$(go list -f '{{.ImportPath}}|{{.Dir}}' ./...)
if [ -z "$list" ]; then
	echo "neutral packages: go list ./... printed nothing" >&2
	exit 1
fi

out=""
while IFS='|' read -r pkg dir; do
	# Git Bash takes C:/a/b but go list prints C:\a\b.
	dir=${dir//\\//}
	if specific "$dir" "$tokens"; then
		want=tested
	else
		want=neutral
	fi
	if [ "$want" = "$mode" ]; then
		out="$out$pkg
"
	fi
done <<EOF
$list
EOF

printf '%s' "$out" | LC_ALL=C sort
