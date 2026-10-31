---
status: designed
date: 2026-10-21
topic: release
amends: steward-rollouts-copy-with-scp.md
amended-by: release-launcher-program.md
formerly: D51, 0051-release-upgrades.md
---

# Releases install side by side, a per-copy pin selects one, and tasks keep the release they started on

## Context

R1.31 needs upgrades and rollback without a Steward and without moving work already under way onto a different binary.
Designed, not built ([release upgrades](../releases.md)).

## Decision

Releases are installed side by side under `releases/<version>/`, each verified against its sha256 before it can run.
A copy-scoped release pin selects the release for new work in that copy only: standalone Directors set it with `guildhall release use`, managed Directors take it from `managed.toml` by approved rollout, and either route records a `release-pin` store event, so no upgrade feature needs a Steward.
A managed pin is applied only after `guildhall config reload` authorizes its revision and verifies its target binary, as the [optional Steward](optional-steward.md) observed-untrusted rule requires.
A fixed `PATH` launcher runs the pinned release; recovery dispatch for an absent or damaged pin is limited to `guildhall release` commands and explicit `guildhall config reload --copy <dir>`, and never authorizes a revision or changes a pin.
Task and Director execution never fall back to another release.
Tasks record their release at creation and every later step dispatches through the launcher, so a pin change reaches new tasks only and rollback is just moving that copy's pin back.
Artisan launches and resumes keep Herdr's plain `guildhall` command; the launcher selects the recorded release.
After `config reload` the Steward runs `guildhall director reload` over SSH, which restarts the Director's agent session at its next step boundary and never mid-step.
`guildhall release prune` runs after every pin change and keeps the newest three installed releases by install time plus every protected release (pins, rollback targets, running or resumable tasks across all registered copies), with no retention setting; each removal is a store event.

## Alternatives rejected

- Telling a Director of a new release by message: the Steward acts by command.
- A strict retention count: deleting a release a task still needs would strand that task.

## Consequences

`director reload` is a second fixed remote command, further narrowing [scp rollouts](steward-rollouts-copy-with-scp.md) as the optional Steward did for `config reload`.
With no verified executable, unsupported schemas or failed authorization, recovery refuses rather than promising repair.
Unreadable or inconsistent copy state blocks pruning, and reference creation is serialized with pruning.
