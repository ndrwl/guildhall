# `internal/render`

## Contract

`Prompt` writes an Artisan attempt's prompt file and skills directory and returns the manifest of every file and digest used; `Question` renders a Guildmaster stage's typed question; skill resolution order, byte-identical copies, tree digests, LF normalisation and slash paths ([rendering](../rendering.md), [Rendered prompts, skills and their digests](../decisions/rendered-prompts-skills-and-digests.md)).
Pure over files: no interface and no double.

## Tests

`render_test.go`: golden prompts and manifests per Artisan kind and golden questions per Guildmaster kind (`testdata/golden`), CRLF and CR sources against real temporary files, refusals
