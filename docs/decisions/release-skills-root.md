# Release skills root

The skills a release ships live in the repository's top-level `skills/` directory, one `guildhall-<name>/` directory per skill, and the Go package there embeds them with `go:embed`.
A release stays a single binary (D1, D5): `cmd/guildhall` wires `skills.Install` into `guildhall render`, which installs a temporary root and passes it to the renderer first as `release`, ahead of the optional Steward-managed and Director roots.
The temporary root is removed after rendering; the attempt retains self-contained skill copies.
Release embedding is delivered here; Steward distribution of skill bundles follows in a later slice.
The directory sits at the module root rather than under `internal/` because it is content that contributors and Stewards read and edit, and `go:embed` only reaches files below the embedding package.
Skill names carry the `guildhall-` prefix so a Director's own skill or a runtime's built-in command never shadows one.
Skill scripts are thin `.sh` and `.ps1` twins with no network access, and no shipped skill needs a script to work.

Skills adapted from mattpocock/skills (MIT) are pinned to commit `5beb35b040e3de97e54fa9e386d7e42d15c295c2`.
`skills/CREDITS.md` lists each adapted skill with its upstream path and reproduces the MIT notice.
Each adapted skill also carries a local `CREDITS.md` with the notice, so every installed and rendered copy carries it.
Each adapted `SKILL.md` body starts with one line naming its source and that commit, outside the description so it costs nothing at launch.
A later re-sync re-pins the commit and updates both.

Skills are checked by Go tests with no model call: frontmatter, names, a description cap, regular files, one sentence per line, golden tree digests and script parsing ([contract](../contracts/skills.md)).
`.gitattributes` checks skills out with LF endings on every platform, because the renderer digests skill files over their exact bytes.
