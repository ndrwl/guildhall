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

## D9. bbolt for the store

The store (R1.17) uses `go.etcd.io/bbolt` v1.5.0, which brings `golang.org/x/sys`.
It is pure Go, so release builds keep `CGO_ENABLED=0`; it gives serializable transactions with a durable commit, and its kernel file lock works on Linux, Windows and macOS.
Records are JSON from the standard library inside bbolt buckets, so the store needs no other codec.
The store has no generic storage interface: `internal/store` exposes concrete record types and mutation methods only.

## D10. Store single-writer lock

The single-writer rule uses bbolt's own file lock and no separate lock file or locking package.
An exclusive lock on the database file for a writer and a shared lock for a read-only open, with a bounded wait, already give the contract the store needs: one writer per file, released by the kernel when the process dies.
The landed gomate `internal/lock` package wraps `github.com/gofrs/flock` for a separate lock file; it would add a dependency and a second lock without a behaviour the store needs, so no gomate code is copied and no notice is added.

## D11. Repository paths are absolute in the owner machine's form

A Steward validates Director configs whose owner machine may run another OS than its own, so `internal/config` accepts a repository `path` that starts with `/` or with a drive letter followed by `/` or `\`, on every host.
Relative, drive-relative and backslash-prefixed UNC paths are refused; paths beginning with `/`, including `//`, are treated as POSIX paths.
Duplicate comparison cleans POSIX dot segments and, for drive paths, normalizes separators, drive-letter case and dot segments independently of the validating host.
A later Director layer that opens repositories on its own machine checks the path against that machine's form.

## D12. Steward rollouts copy files with scp and nothing else

Rollouts in `internal/steward` run only `scp -q -B -o BatchMode=yes -- <source> <destination>` from `PATH`, as an argument list, and no remote command.
This leaves access to the user's SSH config, agent, keys and host-key checking, works the same against POSIX and Windows OpenSSH servers, and avoids any remote shell, whose quoting differs between them.
Roster aliases and copy directories are restricted to characters no remote shell or `scp` interprets, so untrusted roster values cannot inject options or commands.
Atomic replacement would need a remote rename command, so the file is copied in place, then read back and verified by sha256, and the previous revision is copied beside it first.

## D13. Rollout plans, previews and receipts live in a plan directory

A dry run writes `plan.json`, the per-copy previews and, after apply, timestamped receipts into one new directory named by `--plan`.
The plan digest is the sha256 of the plan's JSON encoding with its digest field set to an empty string.
See [apply](steward.md#apply) for revalidation, machine selection, temporary transport inputs and receipt contents.
Receipts are the rollout record for now; they are not a task store.
The managed-settings source marks policy keys with `policy = true`, which is kept privately and not written to copies, because only the Guildmaster can say which preferences are review or approval rules.

## D14. Separate managed publication from Director-owned config

Rollouts write only `managed.toml`, never `config.toml`, so concurrent local edits cannot be overwritten by a stale whole-file snapshot.
See [configuration](configuration.md) for combined loading and legacy inline-layout compatibility.
A checksum framing line detects incomplete managed transfers, but scp publication is non-atomic and may transiently refuse reads; this does not establish uninterrupted config availability.
An exclusive lock file in the private config directory prevents concurrent local applies sharing that directory, not cross-host writers.
Operators must designate one writer and recover a stale lock only after confirming its owner has stopped.
See [Steward revisions and active tasks](steward.md#revisions-and-active-tasks) for revision retention and task binding.

## D15. Local messaging in the store

Director and Artisan messages are records in the Director's existing store, not a separate database, queue, server or file drop.
A send is a store mutation whose id is derived from the message id, so the message, its inbox entry and its receipt commit in one transaction and the store's replay and conflict rules give retries and id reuse their meaning without a second deduplication scheme.
The `messages` and `inbox` buckets are per-repository buckets created on first use, like the other record buckets, so the store stays at schema 1; no release has shipped a schema 1 reader that lacks them.
With no Director or Artisan registry yet, an Artisan's inbox is named by the running stage attempt it works on; a later registry can map onto that identity rather than replace it.
The command line takes the store path explicitly and the envelope from a file or standard input; runtime notifications, daemons and transports remain deferred.

## D16. Contract-first development and package layout

Guildhall is developed contract first: a change states its contract, tests it and then implements it (`docs/contracts.md`).
Packages stay organised by domain under `internal/`, with colocated tests and `testdata/`, following the Go module layout guidance rather than global interface, implementation or test directories.
Interfaces are declared by their consumers at real seams; pure code stays as functions, and the store keeps no generic storage interface (D9) while a consumer may declare a narrow one over the methods it uses.
A test double that replaces a real boundary shares one contract test with the real implementation.

## D17. Herdr through its command-line API

Guildhall drives Herdr by running the `herdr` client with argument lists and reading the JSON it prints, as Herdr's own agent guide documents.
No Go client library for Herdr was found, and the client already handles each platform's transport (a Unix socket, or a named pipe on Windows), so Guildhall does not implement the socket protocol.
Responses recorded from Herdr 0.9.5 are kept under `internal/herdr/testdata` so tests run without Herdr installed.

## D18. Explicit Herdr session and version floor

Every Herdr call carries `--session <name>` and a matching `HERDR_SESSION`, with inherited Herdr context variables removed, so ambient state never chooses the server.
The session-selection and tab-ownership rules are defined in [Herdr access](adapters.md#herdr-access).
0.9.5 is the minimum client and server version because it is the version exercised live; an older or unparsable version is refused.

## D19. Version 1 result file

The [result-file contract](adapters.md#result-file) defines the schema, validation and publication requirements.
Hard-link publication was chosen to make a complete result visible atomically without replacing an existing result on Linux, Windows and macOS.

## D20. Typing the launch command into a new tab

Herdr's tab creation does not take a command, so Guildhall creates the tab, waits until its shell is idle, and types one quoted command line with `pane run`.
Quoting is chosen from the shell Herdr reports for the pane: POSIX single quoting or PowerShell's call operator with single quotes.
An unrecognised shell is refused rather than guessed.
Rendered lines are proven by executing them in each supported shell installed on the test host, and an argument a shell would alter is refused rather than changed.

## D21. Fake adapter as a subcommand

The fake Artisan is `guildhall fake-artisan`, part of the same binary, so CI and lab runs need nothing else installed.
Its test behaviours are selected by `guildhall-fake:` lines in the prompt file, bounded to the outcomes and faults listed in `docs/adapters.md`.

## D22. Workflow approvals per Director, keyed by digest

The Guildmaster's approval of a workflow revision is a store record keyed by the definition's digest, not by name, so an edit always needs a new approval and an approval can never cover content it did not see.
Approvals belong to the Director rather than to a repository or task, so they live in a top-level `workflow-approvals` bucket created on first use; the store stays at schema 1, as in D15.
The mutation id is derived from the digest, so the store's replay and conflict rules make an approval idempotent and refuse different words for the same revision without a second check.
Definitions shipped with a release count as approved by that release without requiring a stored record, so a release that changes its default needs no migration.
See [workflow-edit authority](configuration.md#workflow-edit-authority) for explicit approvals of shipped content and list output.
