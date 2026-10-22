#!/bin/sh
# Prints the script-judged facts of a unified diff for a self-review, with no model and no tool but awk.
# Usage: facts.sh <diff-file> [<budget-lines> [<budget-files>]]; a budget of 0 turns that half of the size flag off.
# Twin of facts.ps1: keep the two equivalent.
set -eu
usage() {
	printf 'usage: facts.sh <diff-file> [<budget-lines> [<budget-files>]]: %s\n' "$1" >&2
	exit 2
}
[ $# -ge 1 ] && [ $# -le 3 ] || usage "one diff file and at most two budgets"
[ -f "$1" ] && [ -r "$1" ] || usage "cannot read $1"
lines=${2:-400}
files=${3:-15}
for n in "$lines" "$files"; do
	case $n in '' | *[!0-9]* | 0?*) usage "budget $n is not a whole number" ;; esac
done
awk -v budget_lines="$lines" -v budget_files="$files" '
function testfile(p) { return p ~ /_test\.go$/ || p ~ /(^|\/)testdata\// }
function item(kind, line) { items[++nitems] = kind " " path ":" line }
function rep(s, n,   r) { r = ""; while (n-- > 0) r = r s; return r }
function secret(s,   l) {
	l = tolower(s)
	return s ~ /-----BEGIN [A-Z ]*PRIVATE KEY-----/ || s ~ ("ghp_" rep("[A-Za-z0-9]", 36)) ||
		s ~ ("AKIA" rep("[0-9A-Z]", 16)) || s ~ ("github_pat_" rep("[A-Za-z0-9_]", 20)) ||
		l ~ ("(api_?key|secret|token|passw(or)?d)[\"\047]?[ \t]*[:=]+[ \t]*[\"\047]" rep("[^\"\047]", 8))
}
function unpinned(s,   ref) {
	if (s !~ /^[ \t-]*uses:/) return 0
	ref = s
	sub(/^[ \t-]*uses:[ \t]*/, "", ref)
	sub(/[ \t]+#.*$/, "", ref)
	gsub(/["\047]/, "", ref)
	if (ref ~ /^\.\//) return 0
	if (ref ~ /^docker:\/\//) return ref !~ /@sha256:/
	return ref !~ ("@" rep("[0-9a-f]", 40) "$")
}
function added(s) {
	if (secret(s)) { secrets++; item("secret", newn) }
	if (go && test) {
		if (s ~ /time\.Sleep\(/) { sleeps_added++; item("sleep-added", newn) }
		if (s ~ /\.Skip(f|Now)?\(|SkipUnlessRequired\(/) { skips_added++; item("skip-added", newn) }
	}
	if (workflow) {
		if (s ~ /pull_request_target/) { workflow_problems++; item("pull-request-target", newn) }
		if (s ~ /^[ \t]*[a-z-]+:[ \t]*write([ \t]|$)/ || s ~ /write-all/) { workflow_problems++; item("write-permission", newn) }
		if (unpinned(s)) { workflow_problems++; item("unpinned-action", newn) }
	}
}
function removed(s) {
	if (go && test) {
		if (s ~ /time\.Sleep\(/) sleeps_removed++
		if (s ~ /\.Skip(f|Now)?\(|SkipUnlessRequired\(/) skips_removed++
		if (s ~ /\.(Error|Errorf|Fatal|Fatalf)\([^)]|\.(Fail|FailNow)\(\)/) { assertions_removed++; item("assertion-removed", oldn) }
	}
}
function setpath(p) { path = p; test = testfile(p); go = p ~ /\.go$/; workflow = p ~ /^\.github\/workflows\// }
{ sub(/\r$/, "") }
oldleft > 0 || newleft > 0 {
	c = substr($0, 1, 1); s = substr($0, 2)
	if (c == "-") { removed(s); oldn++; oldleft--; if (!test) nontest++ }
	else if (c == "+") { added(s); newn++; newleft--; if (!test) nontest++ }
	else if (c == " ") { oldn++; newn++; oldleft--; newleft-- }
	next
}
/^diff --git / {
	p = $0; sub(/^.* b\//, "", p); setpath(p); nfiles++
	if (test) ntest++
	next
}
/^\+\+\+ b\// { setpath(substr($0, 7)); next }
/^@@ / {
	split($2, o, ","); split($3, n, ",")
	oldn = substr(o[1], 2) + 0; oldleft = (2 in o) ? o[2] + 0 : 1
	newn = substr(n[1], 2) + 0; newleft = (2 in n) ? n[2] + 0 : 1
	next
}
END {
	over = (budget_lines > 0 && nontest > budget_lines) || (budget_files > 0 && nfiles > budget_files)
	printf "files: %d\ntest_files: %d\nnontest_lines: %d\n", nfiles, ntest, nontest
	printf "size_budget_lines: %d\nsize_budget_files: %d\nsize_over: %s\n", budget_lines, budget_files, over ? "yes" : "no"
	printf "sleeps_added: %d\nsleeps_removed: %d\nskips_added: %d\nskips_removed: %d\n", sleeps_added, sleeps_removed, skips_added, skips_removed
	printf "assertions_removed: %d\nsecrets: %d\nworkflow_problems: %d\n", assertions_removed, secrets, workflow_problems
	for (i = 1; i <= nitems; i++) print items[i]
}
' "$1"
