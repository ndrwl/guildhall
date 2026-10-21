# `internal/digest`

## Contract

A `Digest` is the sha256 of some bytes, spelled `sha256:<64 lowercase hex>`; one made by `Of`, `Parse` or `ParseHex` is always that spelling.
`Of(data)` digests bytes, `Parse(s)` accepts only the prefixed spelling, `ParseHex(h)` accepts only the bare 64 lowercase hex spelling, and `(Digest).Hex()` is the bare spelling.
`ValidHead(s)` reports whether `s` is a commit object id: 40 (SHA-1) or 64 (SHA-256) lowercase hex characters.
Uppercase hex, the wrong length, a missing, repeated or differently cased prefix, and surrounding space are refused; `Parse` and `ParseHex` name the spelling they expected.
Pure values and functions: no interface and no double.

Each wire format keeps its spelling.
The prefixed spelling is used by workflow, configuration, request, decision subject and review digests, rendered manifests and steward plans; the bare spelling by the store's `DocumentRef.SHA256` and `ArtifactRef.SHA256`, the renderer's `Input.SHA256` and steward file digests.
Both are checked through this package, so a bare value and a prefixed one are converted by `ParseHex` and `Hex` rather than by adding or cutting the prefix by hand.
Mutation ids that embed a digest, such as `<kind>:sha256:<hex>`, are unchanged.

Callers: `store`, `workflow`, `render`, `interp`, `steward`, `config` and `cli`.

## Tests

`digest_test.go` was written red first against a stub that returned zero values: known vectors for `Of`, one table each for `Parse`, `ParseHex` and `ValidHead` covering every refusal above, and the round trip between the two spellings.
The callers' existing validation, digest and golden tests pin their behaviour across the migration: the store's record validation, the workflow review outcome checks, the renderer's manifest goldens, the interpreter's replay and gate tests and the steward plan tests.
Store test helpers were renamed and interpreter test helpers migrated to the shared digest functions; their expectations were not changed.
