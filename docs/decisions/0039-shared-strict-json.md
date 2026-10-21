# D39. One strict JSON decoder shared by Guildhall's formats

Guildhall's JSON formats each refused unknown fields, duplicate keys, trailing data, UTF-16 and newer schemas with their own copy of a key walker and schema check, and a new boundary, the attempt control endpoint, needed the same refusals.
`internal/strictjson` is now the one implementation of those refusals ([contract](../contracts/strictjson.md)), as a plain function with options rather than an interface, because it is pure parsing with a single implementation.
Each format keeps its own schema name, size limit and error sentinel: the package owns only what no format may guess at, and its one sentinel, `ErrNewerSchema`, is what a format returns, wrapped in its own context, for a newer document.
A newer schema is reported before unknown keys or shapes, because a newer version is expected to add them and the useful answer is that the reader is too old.
`messaging.Decode`, which the package was extracted from, and the attempt control endpoint used it first.
The prompt manifest, workflow and review outcome, result-file, steward rollout plan and `guildhall render` request readers moved over next, each keeping its schema name, size limit and error sentinel, with its refusal tests as the pin; their key walkers and schema checks are deleted.
The result file keeps its separate `schema` name and integer `version` fields, so strictjson checks only its shape.
None of the hand-written strict readers this record listed remains; the steward roster and managed settings are TOML and stay outside this package.

The ledger of JSON readers outside this package's formats:

- The attempt's `outcome.json` and `heartbeat.json` are strict: `artisan.ReadOutcome` and `artisan.ReadHeartbeat` decode them with their schema names, `guildhall.attempt-outcome/` and `guildhall.attempt-heartbeat/`, at version 1, so a newer file is told apart from damage and reconcile reads only through them.
- The adapter's `.cancelled` sidecar is a bare `json.Unmarshal` of an identity; it is the launcher's cancel marker ([stop ladder](0041-stop-ladder-precedence.md)), written only by the launcher through `adapter.MarkCancelled` under the attempt lock and read through `adapter.Cancelled` by the launcher and `reconcile.Classify` under the same lock, so the lenient decode is kept by choice: no other process writes the file.
- Documents the engine writes and reads back itself, such as store records, attempt input bindings and revision lists, stay `json.Unmarshal` by choice: no person or other process writes them, and the store's own schema governs their versions.
