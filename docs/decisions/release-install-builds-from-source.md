---
status: accepted
date: 2026-11-03
topic: release
tasks: d127, d149
amends: release-upgrades.md
---

# `release install` builds a missing release from source at its commit, from a configurable source repository, rather than download a binary checked against SHA256SUMS

## Context

[Side-by-side releases](release-upgrades.md) (d127) left installing a release that is not on the machine to `release install <v> --from <file> --sha256 <hex>`, a binary the Guildmaster had to fetch and hash by hand.
The d127 grill agreed a follow-up, d149: download the missing release from the repository's GitHub release and verify it against that release's `SHA256SUMS`.
In d149's grill the Guildmaster asked instead to "download the branch from github and then build it", and to allow forks: a fork has no release assets, and the install-each-merge routine already runs pseudo-versions of main commits that no release publishes.

## Decision

- `guildhall release install <version>` with no `--from` [builds a missing release](../releases.md#building-a-missing-release): it fetches the source with git, resolves the version's exact commit (a tag, or a pseudo-version's 12 hex digits), builds it with a fixed `go build` recipe and installs the result through the same verified `Home.Install`, which must also see the probe report that commit as its `revision`.
  No release asset or `SHA256SUMS` is read; integrity is the git commit hash plus Install's checks.
- The source is `--source`, else the copy's [`release_source`](../configuration.md#settings) setting, local or managed, else `https://github.com/ndrwl/guildhall`; a missing commit refuses with no fallback to another source.
- `release.json` records `source` and `commit` as optional fields of version 1, so older releases still read it.
  `guildhall version` (plain, and `origin` in `--json`) and `release status` show "built from <source> at <commit>" ([version](../contracts/cli.md#version)).
- Guildhall's own repository in a copy's config is the one whose remote is `release_source`, falling back to the module path, so a fork copy gets its latest merge, task and behind-main count against the fork's main.

## Alternatives rejected

- Download the release asset and check it against `SHA256SUMS`, the d127 grill's plan: it works only for published tags of the upstream repository, not for forks or for the main commits copies run between releases.
- A `[release] source` table: it does not fit the flat settings shape and clashes with the managed `release` key, so the setting is the flat `release_source`.
- Let the launcher accept a rebuilt release whose sha256 differs from one a task recorded: that changes the launcher contract, so the messages only say a rebuild may not reproduce the sha256.

## Consequences

Installing needs git and Go on `PATH` and network access for the source and the module proxy, and takes up to ten minutes.
A rebuild is not bit-for-bit another machine's binary, so a task or rollback that recorded another sha256 stays refused, and a managed copy's `config reload` still installs only the rollout's binary against `release_sha256`.
The release workflow and its `SHA256SUMS` are unchanged; prebuilt downloads remain a possible later follow-up.
