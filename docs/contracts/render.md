# `internal/render`

## Contract

`Prompt` and `Question`: the [rendering contract](../rendering.md) (rationale: [Rendered prompts, skills and their digests](../decisions/0025-rendered-prompts-skills-and-digests.md)).
Pure over files: no interface and no double.
`TreeDigest(files []File) digest.Digest` is shared by skill copying and interpreter preparation verification for the [skill tree digest](../rendering.md#skills).
It accepts files in any order without modifying the input slice.

## Tests

`render_test.go`: golden prompts and manifests per Artisan kind and golden questions per Guildmaster kind (`testdata/golden`), CRLF and CR sources against real temporary files, refusals.
`TestTreeDigestIgnoresInputOrder` pins the fixture skill's recorded digest, input-order independence and preservation of the input order.
The independent oracle remains in `skills/skills_test.go`; interpreter preparation refusal cases remain in `internal/interp/prepare_test.go`.
