# `baseline`

## Contract

The release baseline: `roles/director.md`, `roles/artisan.md`, `roles/judge.md` for a delivered task's [judge](quality.md) and one `stages/<kind>.md` for every registry kind an Artisan may run, embedded in the binary and returned by `Release`.
It ships no standards: standards are the Guildmaster's, kept in a copy's own `baseline/standards`.
`Install(dir)` writes the root into a directory that must not exist or must be empty (`ErrNotEmpty`) and leaves nothing behind on error.
An advance resolves each role and guide from the copy's `baseline/` first and then this root, and the manifest records which ([running a task](../running.md#what-an-advance-runs-over)); `guildhall render` uses it for a request that names no role or guide.
The content is runtime-neutral Markdown (R1.21) with no frontmatter, LF endings and one sentence per line.
The Director role states R1.5, the [conversation-first rule](../decisions/0057-director-conversation-first.md), the [live grill](../decisions/0061-grill-live-interview.md), the inbox discipline and the command vocabulary of the README's conversation.

Rationale: [Release baseline](../decisions/0063-release-baseline.md).
Pure over embedded files: no interface and no double.

## Tests

`baseline_test.go`: the root holds exactly the two roles and a guide per Artisan kind of `workflow.Kinds` and no standards; every file is frontmatter-free, LF-only, ends in one newline and has one sentence per line; the sha256sum listing of every file equals `testdata/digests.txt`, with LF checkout enforced by `.gitattributes`; `Install` writes the exact bytes and refuses a non-empty directory.
`internal/cli/baseline_test.go` renders every Artisan stage of the default workflow on a copy holding only `config.toml`, sees a copy's `roles/artisan.md` and standard win over the release with the manifest saying so, refuses a kind with no guide in either root, and refuses rather than replaces a copy file whose stat fails for a reason other than absence (skipped on Windows, which reports that fixture as absent).
`cmd/guildhall` renders a request with no baseline through the built binary and finds the release role and guide in the prompt.
They were written red first ([red run](../evidence/release-baseline-2026-10-22-red.txt)).
No test starts a model.
