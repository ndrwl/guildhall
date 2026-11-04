#!/usr/bin/env bash
# Race-detector shards for CI (docs/decisions/ci-race-shards.md).
#
#   race-shards.sh check       fail unless .github/race-shards.txt puts every
#                              package of `go list ./...` in exactly one shard
#   race-shards.sh packages N  print shard N's import paths
#
# RACE_SHARDS_FILE overrides the shard list, for testing the guard.
set -euo pipefail

shards=3
file=${RACE_SHARDS_FILE:-"$(dirname "$0")/race-shards.txt"}

usage() {
	echo "usage: $0 check | packages <1-$shards>" >&2
	exit 2
}

# entries prints "<shard> <import path>" for each list line, without comments
# or blank lines.
entries() {
	sed -e 's/#.*//' -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' "$file" |
		awk 'NF { print $1, $2, NF }'
}

check() {
	local bad=0 line shard pkg fields
	local -A seen=()
	local -A count=()
	local -A known=()

	if [ ! -r "$file" ]; then
		echo "race shards: cannot read $file" >&2
		exit 1
	fi
	while read -r pkg; do
		known[$pkg]=1
	done < <(go list ./...)

	while read -r shard pkg fields; do
		line="$shard $pkg"
		if [ "$fields" != 2 ]; then
			echo "race shards: $file: malformed line: $line (want \"<shard> <import path>\")" >&2
			bad=1
			continue
		fi
		if ! [[ $shard =~ ^[1-9][0-9]*$ ]] || [ "$shard" -gt "$shards" ]; then
			echo "race shards: $file: $pkg: shard $shard is not 1-$shards" >&2
			bad=1
		fi
		if [ -n "${seen[$pkg]:-}" ]; then
			echo "race shards: $file: $pkg is in more than one shard (${seen[$pkg]} and $shard)" >&2
			bad=1
		else
			seen[$pkg]=$shard
		fi
		if [ -z "${known[$pkg]:-}" ]; then
			echo "race shards: $file: $pkg is not a package of go list ./..." >&2
			bad=1
		fi
		count[$shard]=$((${count[$shard]:-0} + 1))
	done < <(entries)

	for pkg in $(printf '%s\n' "${!known[@]}" | sort); do
		if [ -z "${seen[$pkg]:-}" ]; then
			echo "race shards: $file: $pkg is in no shard; add it to one" >&2
			bad=1
		fi
	done

	if [ "$bad" != 0 ]; then
		exit 1
	fi
	for ((shard = 1; shard <= shards; shard++)); do
		echo "shard $shard: ${count[$shard]:-0} packages"
	done
	echo "total: ${#known[@]} packages"
}

packages() {
	local n=$1
	if ! [[ $n =~ ^[1-9][0-9]*$ ]] || [ "$n" -gt "$shards" ]; then
		usage
	fi
	entries | awk -v n="$n" '$1 == n { print $2 }'
}

case "${1:-}" in
check)
	[ $# -eq 1 ] || usage
	check
	;;
packages)
	[ $# -eq 2 ] || usage
	packages "$2"
	;;
*)
	usage
	;;
esac
