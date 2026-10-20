# `skills`

## Contract

The release skills root: every shipped skill directory and `CREDITS.md`, embedded in the binary and returned by `Release`.
`Install(dir)` writes that root into a directory that must not exist or must be empty (`ErrNotEmpty`), leaves nothing behind on error, and writes files under a skill's `scripts` directory executable.
The binary embeds this package and its rendering command installs the root for use as the first skill root, named `release`.
Steward distribution of skill bundles follows in a later slice.
Every shipped skill keeps these rules, which the tests enforce:

- `SKILL.md` starts with frontmatter of `name` and `description` lines only, `name` equals the directory name and carries the `guildhall-` prefix, and `description` is at most 256 bytes.
- Every file is a regular file with LF line endings, and every Markdown file outside fenced blocks has one sentence per line.
- Each skill's tree digest, as [rendering](../rendering.md#skills) defines it, equals the golden recorded with `sha256sum` in `skills/testdata/digests.txt`.
- Each adapted skill carries `CREDITS.md` with the upstream MIT notice inside its own directory so installed and rendered copies retain attribution.
- A script lives in `scripts/` as a `.sh` and `.ps1` twin, and each parses in its shell.

Rationale: [Release skills root](../decisions/release-skills-root.md).
Pure over embedded files: no interface and no double.

## Tests

`skills_test.go`: the rules above over the embedded and checked-out root, the golden digests, which the Windows runner checks against a CRLF-configured checkout, script parsing with `sh -n` and the PowerShell parser (a missing shell is skipped by name), and `Install` rendered through `render.Prompt` ahead of a decoy managed root.
No test starts a model.
