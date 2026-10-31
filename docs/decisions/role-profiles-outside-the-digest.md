---
status: accepted
date: 2026-10-29
topic: workflow
tasks: d9, d44
amends: task-grants.md
formerly: bound-revision-and-role-profiles.md, no-silent-fake.md
---

# Tasks resolve their bound revision by name, and role profiles are resolved at use, outside the digest, with Guildhall shipping them

## Context

Changing a repository's `workflow` setting stranded every open task with `ErrRevision`, so a repository could never move workflows with tasks in flight.
An Artisan step with no `profile` was refused, so every copy needed a custom workflow naming its profiles before it could run the shipped one (d9).
Defaulting role profiles into the definition fixed that, but put them in the digest, so the shipped `default` and `bug` bound by a copy were no longer the release's bytes and needed a per-release approval (d44).

## Decision

A task's workflow is resolved by its bound revision's name at every advance, `step done` and `step finish`, as at `task new`, with the repository's current overrides, and the digest must still be the bound one ([the bound workflow](../tasks.md#the-bound-workflow)).
An Artisan step that names no `profile` runs under its type's **role profile**, resolved by `workflow.EffectiveProfile` when the step is checked, launched and rendered, and never written into the definition: `research` → `researcher`, `plan` → `planner`, `build` → `builder`, `review` → `reviewer`, and `rebase` → `rebaser` for the merge queue's synthetic rebaser step ([profiles](../configuration.md#profiles)).
A profile written into a step wins, a step overridden to the Guildmaster gets none, and a step type with no role, such as `refute` or `document-review`, must name a profile.
A step that resolves no profile is refused at task admission and before an advance mutates task records, naming the step; there is no default adapter, real or fake, and no test-process exemption, while an explicit `fake` profile stays supported for demos and deterministic tests.
So `task new` binds the shipped `default` and `bug` byte for byte, which the release already authorizes.

Guildhall ships `researcher`, `planner`, `builder`, `reviewer` and `rebaser`, each `adapter = "claude"` and nothing else, and `config.Parse` adds each one a Director config does not declare, so every check that a profile is declared accepts them; a Steward config gets none.
A copy's own `[profile.<name>]` replaces the shipped profile as a whole; one that sets no `allowed_tools` still gets the shipped grant.
The shipped grants are those of [the shipped default grant](task-grants.md), with `git ls-files` added to every role's read-only git and a `rebaser` grant of read-only git plus `add`, `commit`, `fetch` and `rebase`; no shipped profile grants a toolchain.
So every copy runs a claude rebaser Artisan for a `minor_conflicts` rebase, and the escalation to `major_conflicts` remains only for a config built in memory without the profile.

A task bound while role profiles were written into the definition also resolves to that legacy digest, `workflow.ApplyProfileDefaults` over the bare definition with every role declared, which stays only for this comparison.
`task list --workflow <name> --open` lists the open tasks bound to a workflow, so a Director deletes `workflows/<name>.json` only once it prints none; nothing refuses the deletion.

## Alternatives rejected

- A default adapter fallback, real or fake: a task would run on a runtime nobody chose.
- Writing the role profile into the definition: it changed the shipped digests and made a defaulted reviewer a weakening needing approval each release.
- Profiles named after step types: role names keep `claude_skills_profile_<name>` and `quality_judge_profiles` unchanged.
- Merging a copy's profile with the shipped one key by key: replacing it whole is simpler to read in a config file.

## Consequences

The role profile and every profile's settings are outside the digest, so changing the `reviewer` profile, its adapter, model, effort or role prompt, changes who reviews an open or new task without the Guildmaster's approval; the workflow-edit authority guards the workflow's shape, and a profile is the copy's choice of runtime.
A profile written into a guarded step is still part of the definition, so changing it is still a weakening.
A task still strands, naming its bound workflow, when its file or the overrides change, because the store keeps only the digest; shipped revisions are covered by [retired shipped revisions](retired-shipped-revisions.md).
The config digest a task and a Director session store changes for a copy relying on shipped profiles; it is never compared, so nothing strands.
There is no setting to turn the rebaser off.

## History

- 2026-10-26, no-silent-fake.md: refused an Artisan step with no profile, at admission and before an advance mutates records, with no default adapter, real or fake, and no test exemption; it kept the shipped workflow profile-neutral, so copies needed a custom workflow naming real profiles.
  Role profiles later gave every role step a profile, leaving the refusal for step types with no role.
- 2026-10-29, bound-revision-and-role-profiles.md (d9): resolved a task's workflow by its bound name rather than the repository's current setting, and defaulted role profiles into the effective definition, after overrides and before `Validate`, only when the config declared them, so they entered the digest and a defaulted reviewer counted as a weakening; `refute` and `document-review` got no default because no shipped workflow used them.
  The same day (d44) moved role profiles out of the digest and shipped them, because the defaulted shipped digests needed a per-release approval; it kept the legacy digest resolvable so tasks bound under the defaulted digest do not strand, and amended the shipped grants.
