# Retired shipped workflow revisions stay resolvable for the tasks that bound them

A task binds its workflow's name and digest, and every advance, `step finish` and `task grant create` resolves the name again and refuses a definition whose digest is not the bound one (`interp.ErrRevision`).
For `default` and `bug` the name resolves to the definition shipped with the running binary, so an upgrade that changes the shipped bytes strands every open task bound under the earlier release.
That is what stranded d46 and d47 after #166 (48f5f5f3) gave the default's `publish` step an optional `spec` input.

## Retired definitions are pinned bytes

`internal/workflow/retired/` holds the exact bytes each earlier release shipped, copied with `git show <commit>:internal/workflow/<name>.json` and named `<name>-<commit>.json`, where the commit is the last one whose shipped file had those bytes:

- `default-873af971.json`, the default from #18 until #97 (a10f8e3a), and `default-a10f8e3a.json`, the default from #97 until #166 (the revision d46 and d47 bound);
- `bug-8b2ac870.json`, the bug workflow before D95 (729d65d9) dropped its grill and acceptance.

The bytes are stored rather than regenerated, so their digests reproduce exactly; a test pins each file's digest, so a change to the definition structs or their JSON tags that would change it fails loudly.
`workflow.Retired(name)` embeds the directory, parses and validates every file strictly, and returns those named `name`.

The first shipped default, `e4a1ae9e`, is left out: today's `Validate` refuses it (its grill has no `actor` and its review is the removed `automated-review` type), so no task could run it and none can have bound it under a release that validates.

## Resolving a bound revision

`boundWorkflow` first tries the current definition, then its legacy variant with every role profile defaulted ([role profiles outside the digest](role-profiles-outside-the-digest.md)).
When neither matches and the name is shipped, it applies the repository's current `workflow_overrides` to each retired definition of that name, validates it, and tries it and its defaulted variant the same way, returning the first whose digest is the bound one.
The engine's own name and digest check then passes because the returned definition is the bound one.
A task whose overrides changed after it was created still strands, as it does on the current release.
When nothing matches, the error still wraps `ErrRevision` but says the bound digest is not a revision shipped with this or an earlier release, with the repository's current overrides; a workflow file keeps its wording, that its file or the overrides changed.

## The authority boundary

A retired revision is resolvable only for a task that already bound it.
`shippedDefinitions()`, `workflow.Authority.Shipped`, `admitWorkflow` and `workflow approve` and `approvals` stay limited to this release's `default` and `bug`, so a retired revision is never admitted for a new task by release and never listed with source `release`; each retired revision weakens today's, so it is refused unless the Guildmaster approves its exact digest.
Custom workflow files are not archived: keep a workflow's file until every task bound to it has closed.

## Every shipped edit retires the previous bytes

A test pins the digests of the shipped `default.json` and `bug.json`.
Changing either file fails it, with the remedy: copy the previous file's bytes into `retired/<name>-<commit>.json`, pin that file's digest, then update the shipped constant.
This rule applies to every change to a shipped JSON file, however small, since any change to its canonical encoding changes the digest open tasks bound.
