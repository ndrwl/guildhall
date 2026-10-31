---
status: accepted
date: 2026-10-21
topic: foundation
formerly: D39, 0039-shared-strict-json.md
---

# One strict JSON decoder serves Guildhall's JSON formats

## Context

Guildhall's JSON formats each refused unknown fields, duplicate keys, trailing data, UTF-16 and newer schemas with their own copy of a key walker and schema check.
A new boundary, the [attempt control endpoint](endpoint-protocol-v1.md), needed the same refusals.

## Decision

`internal/strictjson` is the one implementation of those refusals ([contract](../contracts/strictjson.md)), as a plain function with options.
Each format keeps its own schema name, size limit and error sentinel: the package owns only what no format may guess at.
Its one sentinel, `ErrNewerSchema`, is what a format returns, wrapped in its own context, for a newer document.
A newer schema is reported before unknown keys or shapes, because a newer version is expected to add them and the useful answer is that the reader is too old.

## Alternatives rejected

- An interface: the package is pure parsing with a single implementation.
- A copy of the key walker and schema check per format: the copies were the problem this replaces.

## Consequences

`messaging.Decode`, which the package was extracted from, and the attempt control endpoint used it first; every other hand-written strict reader moved over next and their key walkers and schema checks are deleted.
Which readers use the package, the result file's versioning exception and the JSON readers that stay outside it by choice are kept on the contract page's [readers ledger](../contracts/strictjson.md#readers).
TOML files, such as the steward roster and managed settings, stay outside this package.
