# `internal/render`

## Contract

`Prompt` and `Question`: the [rendering contract](../rendering.md) (rationale: [Rendered prompts, skills and their digests](../decisions/rendered-prompts-skills-and-digests.md)).
`Session` renders a Director session ([Director sessions](../rendering.md#director-sessions)): the LF role prompt, every skill of the roots byte for byte under the runtime's skills directory, the runtime's files, `startup.md`, with a `Running` section after the in-flight list only when `SessionInput.Running` has items and a `Parked` section after it only when `SessionInput.Parked` has items, and `manifest.json`, refusing a non-empty directory, colliding or non-portable paths and multi-line in-flight, running or parked items, and leaving nothing behind on error; `StartupMessage` names `startup.md` relative to the rendered directory; see [rendering](../rendering.md#director-sessions) for launches outside that directory.
The [Artisan prompt contract](../rendering.md#artisan-prompts) owns the code-review base field.
Pure over files: no interface and no double.
The [required outputs and result template contract](../rendering.md#required-outputs-and-result-template) owns the generated Artisan completion instructions and their actor boundary.
`TreeDigest(files []File) digest.Digest` is shared by skill copying and interpreter preparation verification for the [skill tree digest](../rendering.md#skills).
It accepts files in any order without modifying the input slice.
`DecisionOptions()` returns a copy of the option set each Guildmaster question offers, by the step kind that opens it, and `"design"` for a design round's, for the task page's option-meanings table ([`internal/web`](web.md)).
`BriefSurface` is the one parser of a design brief's first line, `Surface: <surface>` ([design rounds](../interpreter.md#design-rounds)), shared by the request, designer intake and `Prompt`: a brief with no such line reads as `none`, and an unknown surface is an error. `ComposeBrief` writes that line before the Director's text for `task design`, `ParseSurface` checks a flag value and `MockupFile` names a surface's mock-up file.
For a `design` attempt, `Prompt` names the surface of its `brief` input beside it in the Inputs list and, for `web-page`, `cli` or `chat`, adds an "### Also `mockup.html`" or "### Also `mockup.md`" section after the `design` port and lists the mock-up second in the result template; it adds none for `none`, and refuses a brief naming an unknown surface. `DesignQuestion` lists whatever inputs it is given, so the mock-up's digest appears beside the brief and the design.

## Tests

`internal/interp/TestBuiltinOutputInstructions` executes rendering and parses each advertised output with the real consumer, covering exact bound code and document revisions with configured and unconfigured standards.
The configured cases passed against existing rendering; the empty-standard cases failed before the validator fix.

`session_test.go` was written red first against a stub `Session` ([red run](../evidence/director-start-2026-10-22-red.txt)): golden `startup.md` and manifest (`testdata/golden/session-*`), the startup text with nothing in flight, the release skills rendered with the tree digests `skills/testdata/digests/<skill>.sha256` record, the same role digest from CRLF and CR sources, first-root-wins name order, and the refusals.
The notes section ([D94](../decisions/director-reload-and-notes.md)) replaced the handoff-note section in the goldens without a red run: notes of all three kinds, one naming a task and one flagged done, in `session-startup.md`; no notes section in `session-startup-no-notes.md`; the compact warning over `NotesWarnBytes` in `session-startup-notes-over.md` and none at exactly 8 KB; and a multi-line note refused.
`render_test.go`: golden prompts and manifests per Artisan kind and golden questions per Guildmaster kind (`testdata/golden`), CRLF and CR sources against real temporary files, refusals.
`TestBugPromptsCarryTheirInstructions` renders the shipped `bug` workflow's research, plan, build and review steps and finds each step's instructions in its prompt.
`TestPromptFencesTheTitle` sees a title holding a backtick run and a `## ` line quoted in a longer `text` fence before the first section, and no title block for an empty title; the golden prompts carry a title, and the manifest goldens changed only in their prompt digests.
`surface_test.go`: `BriefSurface` over valid, missing and unknown surface lines, CRLF included; the designer prompt for each surface, with the surface beside the brief, the matching "Also" section and result outputs, none for `none` or a pre-surface brief, no surface in other kinds' prompts, an unknown surface refused leaving nothing behind; and `DesignQuestion` listing the mock-up after the design.
`TestPromptNamesItsSlice` renders build and review prompts of slices 1, 2 and 3 of 3 and sees the [slice section](../rendering.md#slice-section) before the role, the entry fenced with a `## Result template` line inside, the merged and later slices, the final-slice wording and the finding line in reviews only; `TestPromptWithoutSlicesHasNoSliceSection` sees none for a nil or one-entry `Stage.Slice`, and the goldens are unchanged (d171).
`TestManifestRecordsBaselineRoots` sees each role, guide and standard keep the root its source names.
`TestTreeDigestIgnoresInputOrder` pins the fixture skill's recorded digest, input-order independence and preservation of the input order.
The independent oracle remains in `skills/skills_test.go`; interpreter preparation refusal cases remain in `internal/interp/prepare_test.go`.
