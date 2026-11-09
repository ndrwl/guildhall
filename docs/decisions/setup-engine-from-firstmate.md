---
status: accepted
date: 2026-11-04
topic: steward
tasks: d128
---

# Machine setup runs on one Go engine of declarative step files, ported from Firstmate's wizard and doctor scripts

## Context

The Steward is to set up Director machines end to end (d128), and some of that setup is work only a person can do: administrator installs, sign-ins, WSL settings.
Firstmate had already solved both halves for its own machines in shell: `wizard-template.sh` and its twin `wizard-template.ps1` walk a person through numbered steps, `fm-remote-doctor.sh` reports a machine's gaps, each tagged fixable or human with an exact action line, and `fm-install-herdr.sh` installs Herdr into the user's directory.
An earlier d128 plan adopted those scripts as a bash and PowerShell `setup/` fork.
The Guildmaster asked instead for Go versions, and whether they would let the Steward write custom installation scripts; on hearing yes they accepted the plan (d128's context, 2026-11-04).

## Decision

`internal/setup` is a one-time port, owned by Guildhall, of those three scripts' behaviour into Go, run by `guildhall setup run|wizard|doctor` ([setup](../setup.md)).

- Every install, built in or written by the Steward, is a declarative **step file** (`guildhall.steps/1`): numbered steps, each with an id, a title, a `fixable` or `human` tag, an os list, a check, an act, an optional verify and an undo note. Commands are argv lists run with no shell; a step that needs a script names `shell = "sh"` or `"pwsh"` with a `script` body, which is how a Steward-written step file carries a custom installation.
- A step file's digest is the sha256 of its bytes. A file named by path runs only under `--digest` with its own digest; the built-in files (`herdr`, `go`, `git-and-claude`, `sign-in`, `wsl-settings`) are embedded in the binary.
- The wizard keeps Firstmate's numbered cards, `confirm`, `ask` and masked `ask_secret` prompts, recorded actions, undo note on failure, `--dry-run`, journal and completion record, with one implementation for every operating system and no secret value in any file it writes.
- The doctor keeps the fixable-or-human tags, exact action lines, `--fix` closing fixable gaps only and re-deriving every check after; it adds `--json`, gap ids that are step ids, `--fix --only` and an ownership manifest outside which it overwrites nothing.
- The Herdr step keeps the user-directory, idempotent install verified with `herdr --version`, takes its version, URL and sha256 from the caller, refuses a wrong checksum and never touches a running Herdr server.
- Credit: [`NOTICE`](../../NOTICE) carries Firstmate's and `mattpocock/skills`' MIT notices, and each ported Go file names the Firstmate file it follows.
  The ported files and their sources are `internal/setup/wizard.go` and `term.go` (`wizard-template.sh`, `wizard-template.ps1`), `internal/setup/doctor.go` (`fm-remote-doctor.sh`), and `internal/setup/fetch.go` with `internal/setup/builtin/herdr.toml` (`fm-install-herdr.sh`).
  The revisions they follow: `bin/fm-remote-doctor.sh` and `bin/fm-install-herdr.sh` (with `docs/remote-secondmates.md`) at `ndrwl/firstmate` commit `2add138ffe92309787dbbedf375beb4f05704f7b`; `bin/wizard-template.sh` and `bin/wizard-template.ps1` at `ndrwl/dev-setup` commit `8a095ccfeff5563f776f743d088b44c5afc57747`, the wizard template Firstmate uses, which lives in `dev-setup` rather than in Firstmate itself and was adapted from `mattpocock/skills` at `49dd158` (MIT, Matt Pocock).
  The Director looked these up for d128 (its context, 2026-11-04).
- Later Firstmate changes are picked up by hand; no test pins Firstmate's scripts (d128's Q6 (a)).

## Alternatives rejected

- The bash and PowerShell fork: a `.sh` and `.ps1` pair per script drifts apart, cannot be tested on one runner, and would put a second setup implementation beside Guildhall's Go one.
- Copying the scripts unchanged: they have no digest check, no JSON report and no ownership manifest, which an approved plan needs.
- A test that fails whenever Firstmate's scripts change: the port is a rewrite with Guildhall-specific changes, so it would flag every Firstmate edit without saying whether it matters.

## Consequences

One engine covers Linux, macOS and native Windows; each built-in step has a Windows form of the same id in the same file, in place of Firstmate's separate `.ps1` script, so the two forms are reviewed side by side.
The engine runs any check, act and script a step file names, so a step file's safety rests on its digest being approved; the engine never runs `sudo`, and a step that needs administrator rights is tagged `human`.
d128's provisioning slice builds on it: the Steward runs `setup doctor --json` on a machine, and plans each gap as a fix or a wizard the Guildmaster runs.
