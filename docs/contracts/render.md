# `internal/render`

## Contract

`Prompt` and `Question`: the [rendering contract](../rendering.md) (rationale: [Rendered prompts, skills and their digests](../decisions/rendered-prompts-skills-and-digests.md)).
Pure over files: no interface and no double.

## Tests

`render_test.go`: golden prompts and manifests per Artisan kind and golden questions per Guildmaster kind (`testdata/golden`), CRLF and CR sources against real temporary files, refusals
