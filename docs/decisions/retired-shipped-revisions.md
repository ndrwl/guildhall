---
status: accepted
date: 2026-10-30
topic: workflow
tasks: d46, d47, d67
---

# Retired shipped workflow revisions stay resolvable for the tasks that bound them

## Context

A task binds its workflow's name and digest, and every advance, `step finish` and `task grant create` resolves the name again and refuses a definition whose digest is not the bound one (`interp.ErrRevision`).
For `default` and `bug` the name resolves to the definition shipped with the running binary, so an upgrade that changes the shipped bytes strands every open task bound under the earlier release.
That stranded d46 and d47 after #166 (48f5f5f3) gave the default's `publish` step an optional `spec` input.

## Decision

`internal/workflow/retired/` holds the exact bytes each earlier release shipped, copied with `git show <commit>:internal/workflow/<name>.json` and named `<name>-<commit>.json`, the commit being the last whose shipped file had those bytes: `default-873af971.json` (from #18 until #97), `default-a10f8e3a.json` (from #97 until #166, the revision d46 and d47 bound) and `bug-8b2ac870.json`, the bug workflow before it dropped its grill and acceptance ([task kinds](task-kinds.md)).
`workflow.Retired(name)` embeds the directory, parses and validates every file strictly, and returns those named `name`.

`boundWorkflow` tries the current definition, then its legacy variant with every role profile defaulted ([role profiles outside the digest](role-profiles-outside-the-digest.md)).
When neither matches and the name is shipped, it applies the repository's current `workflow_overrides` to each retired definition of that name, validates it, and tries it and its defaulted variant, returning the first whose digest is the bound one, so the engine's own name and digest check passes.
When nothing matches, the error still wraps `ErrRevision` and says the bound digest is not a revision shipped with this or an earlier release, with the repository's current overrides; a workflow file keeps its wording, that its file or the overrides changed.

A retired revision is resolvable only for a task that already bound it: `shippedDefinitions()`, `workflow.Authority.Shipped`, `admitWorkflow`, `workflow approve` and `approvals` stay limited to this release's shipped workflows, so a retired revision is never admitted for a new task by release; each weakens today's, so it is refused unless the Guildmaster approves its exact digest.

A test pins the digests of the shipped `default.json` and `bug.json`, and changing either fails it with the remedy: copy the previous bytes into `retired/<name>-<commit>.json`, pin that file's digest, then update the shipped constant.
This applies to every change to a shipped JSON file, however small, since any change to its canonical encoding changes the digest open tasks bound.

## Alternatives rejected

- Regenerating retired definitions from code: stored bytes reproduce their digests exactly, and a test pins each, so a change to the definition structs or JSON tags fails loudly.
- Keeping the first shipped default, `e4a1ae9e`: today's `Validate` refuses it (its grill has no `actor` and its review is the removed `automated-review` type), so no task under a validating release can have bound it.
- Archiving custom workflow files: keep a workflow's file until every task bound to it has closed.

## Consequences

A task whose overrides changed after it was created still strands, as on the current release.
Every edit to a shipped workflow now carries a retired file and a pinned digest.
