# `internal/strictjson`

## Contract

`Decode(data, v, Options{Schema, Version, MaxBytes})` decodes one JSON document into `v` exactly as `v`'s type declares it, or refuses it and leaves `v` unchanged ([decision](../decisions/shared-strict-json.md)).
It refuses a document over `MaxBytes` (zero means no bound), UTF-16 with a hint to write UTF-8 with `Set-Content -Encoding utf8`, invalid UTF-8 or JSON, an unpaired surrogate escape, trailing data, a duplicate key at any depth, and a key the target type does not declare.
Keys are compared exactly with the json name, case included, in structs, map values, slices, arrays and through pointers; an interface value or a type that decodes itself, such as `json.RawMessage`, is checked for duplicates only, and an untagged embedded struct's fields are refused rather than promoted.
A leading UTF-8 byte order mark is dropped.
With `Schema` set, the top-level `schema` must be `<Schema><n>` with `n` a positive number in canonical form; `n` above `Version` is `ErrNewerSchema`, the one sentinel, reported even when the newer document also has unknown keys or shapes.
See [`Options.Schema`](../../internal/strictjson/strictjson.go) for decoding without schema-version validation.
Every other error means the document is malformed; each caller adds its own sentinel, context, limit and schema name.

Callers today: `messaging.Decode` and `workflow.Parse` and `workflow.ParseReviewOutcome` (whose `ErrNewerSchema` is this sentinel), the [attempt control endpoint](../artisan.md#protocol) for requests and replies, the interpreter's prompt manifest reader, `adapter.ParseResult` (shape only: the result file versions with its own `version` field), `steward.ReadPlan` and the `guildhall render` request.

## Tests

`strictjson_test.go` was written red first against a stub that accepted everything: one table per rule (size bound, encodings, surrogates, duplicate keys, unknown fields, trailing data, schema), plus valid documents, an unchanged target on refusal and non-object targets.
`TestDecodeShellFixtures` reuses the CRLF, BOM and UTF-16 envelopes in `internal/messaging/testdata`.
The messaging envelope tests pin `messaging.Decode`'s behaviour across the migration and passed unchanged.
The later migrations kept each reader's refusal tests as the pin: `TestInvalidPreparationIsRefused`, `TestSchemaRefusals`, `TestExactJSONFields` and the result-file refusal table passed unchanged, while the manifest's duplicate-key and newer-schema cases, `TestReadPlanIsStrict` and `TestRenderRefusesMalformedRequestBeforeInstallation` were added red first; one CLI diagnostic pin now names strictjson's "not one JSON value" instead of the deleted "malformed JSON" wording.
