# `internal/render`

## Contract

`Prompt` and `Question`: the [rendering contract](../rendering.md) (rationale: [Rendered prompts, skills and their digests](../decisions/0025-rendered-prompts-skills-and-digests.md)).
`Session` renders a Director session ([Director sessions](../rendering.md#director-sessions)): the LF role prompt, every skill of the roots byte for byte under the runtime's skills directory, the runtime's files, `startup.md` and `manifest.json`, refusing a non-empty directory, colliding or non-portable paths and multi-line in-flight items, and leaving nothing behind on error; `StartupMessage` names `startup.md` relative to the rendered directory; see [rendering](../rendering.md#director-sessions) for launches outside that directory.
The [Artisan prompt contract](../rendering.md#artisan-prompts) owns the code-review base field.
Pure over files: no interface and no double.
The [required outputs and result template contract](../rendering.md#required-outputs-and-result-template) owns the generated Artisan completion instructions and their actor boundary.
`TreeDigest(files []File) digest.Digest` is shared by skill copying and interpreter preparation verification for the [skill tree digest](../rendering.md#skills).
It accepts files in any order without modifying the input slice.

## Tests

`internal/interp/TestBuiltinOutputInstructions` executes rendering and parses each advertised output with the real consumer, covering exact bound code and document revisions with configured and unconfigured standards.
The configured cases passed against existing rendering; the empty-standard cases failed before the validator fix.

`session_test.go` was written red first against a stub `Session` ([red run](../evidence/director-start-2026-10-22-red.txt)): golden `startup.md` and manifest (`testdata/golden/session-*`), the startup text with no handoff note and nothing in flight, the release skills rendered with the tree digests `skills/testdata/digests.txt` records, the same role digest from CRLF and CR sources, first-root-wins name order, and the refusals.
`render_test.go`: golden prompts and manifests per Artisan kind and golden questions per Guildmaster kind (`testdata/golden`), CRLF and CR sources against real temporary files, refusals.
`TestManifestRecordsBaselineRoots` sees each role, guide and standard keep the root its source names.
`TestTreeDigestIgnoresInputOrder` pins the fixture skill's recorded digest, input-order independence and preservation of the input order.
The independent oracle remains in `skills/skills_test.go`; interpreter preparation refusal cases remain in `internal/interp/prepare_test.go`.
