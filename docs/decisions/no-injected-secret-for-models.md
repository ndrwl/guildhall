---
status: accepted
date: 2026-11-04
topic: steward
tasks: d130
---

# Model-driven processes get no injected secret except the names a profile's inherit_env lists

## Context

The session supervisor decrypts the machine's [secrets](../secrets.md) into its own environment, so the `gh` and `git` it runs can use them.
Every process it starts would inherit them, including the Director's runtime, which starts every Artisan, so any model could read and repeat any secret (d130).

## Decision

Model-driven processes, the Director's or Steward's runtime, every Artisan's launcher and worker, the pane bridge and their descendants, get no injected secret by default.
The supervisor lists the names, never the values, in `GUILDHALL_SECRET_NAMES`, and each of their environments is built without those names (`secrets.Strip`).
A profile's new `inherit_env` key lists the names its Artisans get: the launcher decrypts the file itself for them, and the runtime wrapper redacts their values from `launcher.log`, the Workbench event file and the result.
`inherit_env`'s entries are variable names, never values, so the key passes config validation, which still refuses a credential-like setting key.

## Alternatives rejected

- Pass every secret to every process: any model could read any secret, and any log could hold one.
- Pass secrets to the Director's runtime and let it choose: the choice would be a model's.
- Redaction alone: it misses a value a runtime transforms or splits, so it is kept as defence in depth only.

## Consequences

An Artisan that needs a key, such as for a repository's integration tests, needs its profile to list it.
This keeps values away from models by default; it is no sandbox, since a model-driven process runs as the machine's user, who can read the key.
