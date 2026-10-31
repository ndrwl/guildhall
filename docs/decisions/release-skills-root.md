---
status: accepted
date: 2026-10-21
topic: release
formerly: D30, 0030-release-skills-root.md
---

# Release skills live in the top-level `skills/` directory and are embedded in the binary

## Context

A release must ship the skills its sessions use while staying a single binary ([one binary](one-go-module-and-one-binary.md), [release builds](release-builds.md)), and some skills are adapted from mattpocock/skills under the MIT licence.

## Decision

The skills a release ships live in the repository's top-level `skills/` directory, one `guildhall-<name>/` directory per skill, and the Go package there embeds them with `go:embed`.
`cmd/guildhall` wires `skills.Install` into `guildhall render`, which installs a temporary root and passes it to the renderer first as `release`, ahead of the optional Steward-managed and Director roots.
The temporary root is removed after rendering; the attempt retains self-contained skill copies.
Skill names carry the `guildhall-` prefix so a Director's own skill or a runtime's built-in command never shadows one.
Skill scripts are thin `.sh` and `.ps1` twins with no network access, and no shipped skill needs a script to work.

Adapted skills are pinned to upstream commit `5beb35b040e3de97e54fa9e386d7e42d15c295c2`.
`skills/CREDITS.md` lists each adapted skill with its upstream path and reproduces the MIT notice, and each adapted skill carries a local `CREDITS.md` with the notice, so every installed and rendered copy carries it.
Each adapted `SKILL.md` body starts with one line naming its source and that commit, outside the description so it costs nothing at launch.

Skills are checked by Go tests with no model call ([contract](../contracts/skills.md)).
`.gitattributes` checks skills out with LF endings on every platform, because the renderer digests skill files over their exact bytes.

## Alternatives rejected

- `skills/` under `internal/`: the skills are content that contributors and Stewards read and edit, and `go:embed` only reaches files below the embedding package.

## Consequences

Release embedding was delivered with this decision; Steward distribution of skill bundles follows in a later slice.
A later re-sync of adapted skills re-pins the commit and updates both credit files and the source lines.
The [release baseline](release-baseline.md) follows the same pattern for role prompts and stage guides.
