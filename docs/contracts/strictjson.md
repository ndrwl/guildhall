# `internal/strictjson`

## Contract

`Decode(data, v, Options{Schema, Version, MaxBytes})` decodes one JSON document into `v` exactly as `v`'s type declares it, or refuses it and leaves `v` unchanged ([decision](../decisions/shared-strict-json.md)).
It refuses a document over `MaxBytes` (zero means no bound), UTF-16 with a hint to write UTF-8 with `Set-Content -Encoding utf8`, invalid UTF-8 or JSON, an unpaired surrogate escape, trailing data, a duplicate key at any depth, and a key the target type does not declare.
Keys are compared exactly with the json name, case included, in structs, map values, slices, arrays and through pointers; an interface value or a type that decodes itself, such as `json.RawMessage`, is checked for duplicates only, and an untagged embedded struct's fields are refused rather than promoted.
A leading UTF-8 byte order mark is dropped.
With `Schema` set, the top-level `schema` must be `<Schema><n>` with `n` a positive number in canonical form; `n` above `Version` is `ErrNewerSchema`, the one sentinel, reported even when the newer document also has unknown keys or shapes.
See [`Options.Schema`](../../internal/strictjson/strictjson.go) for decoding without schema-version validation.
Every other error means the document is malformed; each caller adds its own sentinel, context, limit and schema name.

## Readers

`messaging.Decode`, which the package was extracted from, and the attempt control endpoint used it first.
The prompt manifest, workflow and review outcome, result-file, steward rollout plan and `guildhall render` request readers moved over next, each keeping its schema name, size limit and error sentinel, with its refusal tests as the pin; their key walkers and schema checks are deleted.
The result file keeps its separate `schema` name and integer `version` fields, so strictjson checks only its shape.
No hand-written strict reader remains; the steward roster and managed settings are TOML and stay outside this package.

The JSON readers outside this package's formats:

- The attempt's `outcome.json` and `heartbeat.json` are strict: `artisan.ReadOutcome` and `artisan.ReadHeartbeat` decode them with their schema names, `guildhall.attempt-outcome/` and `guildhall.attempt-heartbeat/`, at version 1, so a newer file is told apart from damage and reconcile reads only through them.
- The adapter's `.cancelled` sidecar is a bare `json.Unmarshal` of an identity; it is the launcher's cancel marker ([stop ladder](../decisions/stop-ladder-precedence.md)), written by the owned launcher through `adapter.MarkCancelled` and read through `adapter.Cancelled` for launcher/reconcile decisions under the attempt lock.
  The retained Windows Herdr-tab path also writes it through `OwnedWorker.Stop` without that lock; worker and adapter readers check it outside the lock during startup, polling and result handling.
  The lenient identity decode is kept by choice; the sidecar is not a separate, retired format.
- Documents the engine writes and reads back itself, such as store records, attempt input bindings and revision lists, stay `json.Unmarshal` by choice: no person or other process writes them, and the store's own schema governs their versions.

## Tests

`strictjson_test.go` was written red first against a stub that accepted everything: one table per rule (size bound, encodings, surrogates, duplicate keys, unknown fields, trailing data, schema), plus valid documents, an unchanged target on refusal and non-object targets.
`TestDecodeShellFixtures` reuses the CRLF, BOM and UTF-16 envelopes in `internal/messaging/testdata`.
The messaging envelope tests pin `messaging.Decode`'s behaviour across the migration and passed unchanged.
The later migrations retain reader-level refusal coverage in `TestInvalidPreparationIsRefused`, `TestSchemaRefusals`, `TestExactJSONFields` and the result-file refusal table.
`TestInvalidPreparationIsRefused` now also covers duplicate keys, nested unknown fields, UTF-16, unpaired surrogates and the newer-schema sentinel.
`TestReadPlanIsStrict` and `TestRenderRefusesMalformedRequestBeforeInstallation` cover the plan and command-line boundaries; one CLI diagnostic pin now names strictjson's "not one JSON value" instead of the deleted "malformed JSON" wording.
