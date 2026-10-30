# Version information from build info, shown first in the Director chat

`guildhall version` printed one line, such as `guildhall v0.0.0-20261027210024-abd974855ac9 linux/amd64 go1.27.2`, which did not say which merges a binary includes.
The Guildmaster installs and restarts after batches of merges and wanted to see, in the Director chat at every session start, which revision the `guildhall` binary is, which pull request it ends at and how far it is behind main ([contract](../contracts/cli.md#version)).

## Build info over linker flags

The revision, its commit time and the modified flag come from the `vcs.*` settings Go stamps into every `go build` or `go install` from a git checkout, read with `debug.ReadBuildInfo`.
No build script, Makefile or linker flag is needed, so a plain `go install ./cmd/guildhall` keeps working, and the release workflow's injected tag still wins in `Version()`, so `doctor` and the session record's `release` do not change.
A build without `vcs.*` from the module proxy at a pseudo-version, `go install …@v0.0.0-<yyyymmddhhmmss>-<12 hex>`, takes the abbreviated revision and its commit time from that version, so its latest merge and how far it is behind main are still shown: git resolves the 12-digit abbreviation in Guildhall's own repository, and leaves the repository fields empty when it cannot.
It is never modified, as the module proxy builds from a clean tree; the Go module tooling's own parser, `golang.org/x/mod`, is not a dependency, so a small local pattern reads the three pseudo-version forms.
Any other build without `vcs.*`, a tagged release from the module proxy, with `-buildvcs=false` or from a tree without `.git`, shows the release only.

`vcs.modified` is what Go reports: on Go 1.27 a checkout whose only changes are untracked files builds as modified, so the flag reads as "modified (uncommitted or untracked files)" and `--startup` words its warning that way.

## The repository by its remote

The binary knows its module path, `github.com/ndrwl/guildhall`, but not its source directory, especially under `-trimpath`.
A copy may list many repositories and none is marked as Guildhall's own, so `version` uses the one whose `remote` names the module path once the URL scheme, user, the colon of `git@host:path` and a trailing `.git` are dropped.
No match or several give no repository fields; no config key was added.

## The task from store documents

Squash merges end their subject with ` (#N)`, which gives the pull request.
GitHub's squash message drops the body naming the Guildhall task, so the task comes from the copy's own delivery documents, `tasks/<repo>/<task>/<step>/pull-request.json`, which record the pull request number.
That needs no network and no forge, at the cost that pull requests this copy did not deliver show no task; a `gh pr view` fallback is left for later.

## A bounded fetch into a private ref

Behind main needs the remote's default branch.
Plain `version` and `--startup` fetch it into `refs/guildhall/version/main` with every git run bounded at five seconds, and record the fetch time in the copy's `state/version-fetch.json`, since a fetch that does not move the ref leaves no trace in git itself.
A failed fetch keeps the old ref and says "as of" its recorded time; with no ref there is no count.
`--json` and `director status` never fetch, so the status feed the Director runs at every turn stays local, and its `as_of` says how old the count is.
The count follows first parents, so a merge commit on main counts once, which for this repository's squash-only history is every commit.

## Option (a): one first message

A Claude Code Director session gets exactly one launch message, after `--`, and no second channel at launch (a foreground start has no pane to type into).
So the first message is `/version startup "<absolute startup.md path>"`: the session's `/version` command, `.claude/commands/version.md`, which the Claude adapter writes beside its settings, has the model run `guildhall version --startup`, reply with only its output, and then read and follow `startup.md`, all in the one turn the safety checks count.
A release skill would be named `/guildhall-version`, so the command is an adapter file, not a skill.
The fake runtime and runtimes that start in the session directory keep `render.StartupMessage`.

Whether Claude Code substitutes `$1` or `$ARGUMENTS` inside a `` !`…` `` pre-execution line could not be checked by hand in this build, which could not run `claude`.
The command therefore uses the fallback the spec allows: it asks for one Bash call, which the session's settings and the command's `allowed-tools: Bash(guildhall version:*)` permit without a prompt.
It costs one tool call more than pre-execution would; switching to pre-execution once confirmed changes only the command file.

The `/version` the session runs is the `guildhall` on `PATH`, which may be newer than the supervisor after an install without a restart.
`--startup` compares its own `Version()` with the `release` the current session's supervisor recorded, the session numbered by `GUILDHALL_SESSION`, read-only from the store, and warns when they differ.
Warnings never block startup or change the exit code.
