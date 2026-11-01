# `skills`

## Contract

The release skills root: every shipped skill directory and `CREDITS.md`, embedded in the binary and returned by `Release`.
`Install(dir)` writes that root into a directory that must not exist or must be empty (`ErrNotEmpty`), leaves nothing behind on error, and writes files under a skill's `scripts` directory executable.
The binary embeds this package and its rendering command installs the root for use as the first skill root, named `release`.
Steward distribution of skill bundles follows in a later slice.
Every shipped skill keeps these rules, which the tests enforce:

- `SKILL.md` starts with frontmatter of `name` and `description` lines only, using nonempty plain YAML scalars without leading indicators, mapping separators or inline comments; `name` equals the directory name and carries the `guildhall-` prefix, and `description` is at most 256 bytes.
- Every file is a regular file with LF line endings, and every Markdown file outside fenced blocks has one sentence per line.
- Each skill's tree digest, as [rendering](../rendering.md#skills) defines it, equals its golden `skills/testdata/digests/<skill>.sha256`, one `sha256sum`-format line per skill; a skill with no file and an orphan file each fail.
- Each adapted skill, one whose body names its upstream source, carries `CREDITS.md` with the upstream MIT notice inside its own directory, listing its source, so installed and rendered copies retain attribution; a skill adapted from nothing carries none.
- A script lives in `scripts/` as a `.sh` and `.ps1` twin, and each parses in its shell.
- `guildhall-contract-tdd`'s `check` twins run exactly `gofmt -l .`, `go vet ./...` and `go test ./...` in that order with `GOPROXY=off` and `GOTOOLCHAIN=local`, fail when `gofmt` lists a file, stop at the first failing command and exit non-zero, and both twins make the same calls and the same pass or fail decision.
- The Director's skills, `guildhall-director-startup`, `guildhall-director-inbox`, `guildhall-director-tasks`, `guildhall-director-stage`, `guildhall-handoff` and `guildhall-maintenance`, together name every command of the README's command list that the Guildmaster would otherwise type, as `guildhall <command>`, so [plain English is the primary interface](../decisions/director-conversation-first.md); the commands the Guildmaster never asks the Director for are listed in the test with why.
  `guildhall-director-inbox` names the four `director note` commands, with the habit of writing a note in the same turn something arises that no record holds ([D94](../decisions/director-reload-and-notes.md)).
- The Steward's skills are `guildhall-steward-startup`, which a Steward session uses when it starts to read its notes, work every in-flight rollout approval and run `guildhall steward ready`, and `guildhall-steward-rollout`, which the Steward uses when the Guildmaster asks to roll out shared settings, check the roster or see what a copy last received, to dry-run, get approval of the exact plan digest and apply it ([Steward](../steward.md)).
  The README's `steward rollout`, `steward status` and `steward approval request` commands, which they name, are among those the Director skills' README-command test lists as not the Director's.
- `guildhall-self-review`'s `facts` twins read a unified diff with no model call and no tool but the shell and awk, print the same counts and `file:line` items for the same diff and budget, flag a change over either nonzero budget, and exit 2 naming the problem for a missing diff or a budget that is not a whole number; its example document parses as the `guildhall.self-review/1` schema, with the facts script's keys and each criterion carrying the field its kind and judge record.
- `guildhall-review` holds only `SKILL.md`, `smells.md` and `CREDITS.md`, with no scripts and no sub-agent definitions; its example outcome parses as a valid [review outcome](../configuration.md#review-outcome) representing a blocking round with dispositions and a rule-less suggestion.

Rationale: [Release skills root](../decisions/release-skills-root.md).
Pure over embedded files: no interface and no double.

## Tests

`skills_test.go`: the rules above over the embedded and checked-out root, the golden digests on the CI platform matrix with LF checkout enforced by `.gitattributes`, script parsing with `sh -n` and the PowerShell parser, the `check` twins run in those shells against the test binary acting as `go` and `gofmt` in a clean run and each failure case (a missing shell is skipped by name in both, and fails a runner that requires it), the `facts` twins run on the fixture diff `testdata/self-review/fixture.diff` against its golden `fixture.facts` with each budget case and refusal, the `guildhall-self-review` example document parsed into its typed schema, the `guildhall-review` example outcome through `workflow.ParseReviewOutcome`, the Director skills' coverage of the README commands, and `Install` rendered through `render.Prompt` ahead of a decoy managed root.
No test starts a model.
`TestAdaptedSkillsCarryCredits` replaced the render test's check that every skill carries the notice, which the Director's own skills, adapted from nothing, would have failed.
`TestSelfReviewFactsTwinsAgree` was written red first against stub scripts ([red run](../evidence/quality-rubric-2026-10-22-red.txt)).
`TestDirectorSkillsNameEveryReadmeCommand` was written red first ([red run](../evidence/director-skills-2026-10-22-red.txt)), before the Director's skills existed.
The other direction, that every command a skill names is one `guildhall help` lists, is the [CLI's](cli.md#tests) `TestHelpNamesReadmeCommands`, which owns the help text.
