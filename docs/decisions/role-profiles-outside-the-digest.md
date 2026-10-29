# Role profiles are resolved at use, outside the digest, and Guildhall ships them

This decision supersedes the digest parts of [tasks resolve their bound revision, and Artisan steps default to role profiles](bound-revision-and-role-profiles.md): that the role profile default is applied into the effective definition, is part of its digest, and makes a defaulted reviewer's profile a weakening.
It also supersedes the last line of [task kinds](task-kinds.md): the shipped `bug` workflow needs no approval, so `workflow approve --config --repo --kind` approves a repository's own overrides of a kind's workflow, not the shipped definition.

## Role profiles leave the digest

An Artisan step that names no `profile` runs under its type's **role profile**, resolved when the step is checked, launched and rendered by `workflow.EffectiveProfile`, and never written into the definition.
The table is `research` → `researcher`, `plan` → `planner`, `build` → `builder`, `review` → `reviewer`, and `rebase` → `rebaser` for the merge queue's synthetic rebaser step, which no workflow names.
A profile written into a step wins, a step overridden to the Guildmaster gets none, and a step type with no role, such as `refute` or `document-review`, still has to name a profile and is refused without one.
`task new` therefore binds the shipped `default` and `bug` revisions byte for byte, which the release already authorizes, so neither needs a per-release approval.

What the role profile is, and the settings of every profile, shipped or declared, are outside the digest like the files a profile points to.
So changing the `reviewer` profile, its adapter, model, effort or role prompt, changes who reviews an open or new task without the Guildmaster's approval.
This is accepted: the workflow-edit authority guards the shape of the workflow, and a profile is the copy's choice of runtime.
A profile written into a guarded step is still part of the definition, so changing it is still a weakening the Guildmaster approves.

## Shipped profiles

Guildhall ships `researcher`, `planner`, `builder`, `reviewer` and `rebaser`, each `adapter = "claude"` and nothing else, so each runs the adapter's default model with its shipped default grant.
`config.Parse` adds each one a Director config does not declare to its profiles before validating it, so every check that a profile is declared accepts the shipped names unchanged: repository grants, `claude_skills_profile_<name>`, `quality_judge_profiles`, the `director start` preflight and the skill policy, and `claude.EffectiveGrant`.
A Steward config gets none.
A copy's own `[profile.<name>]` replaces the shipped profile as a whole, not key by key, which is the simplest rule to read in a config file; one that sets no `allowed_tools` still gets the shipped grant, so choosing a model or effort keeps the permissions.
With every role covered, a role step always resolves a profile, and a copy with no `[profile.*]` section runs the shipped workflows.

The shipped grants are those of [D93](0093-shipped-default-grants.md), with `git ls-files` added to the read-only git every role gets and a `rebaser` grant: read-only git plus `add`, `commit`, `fetch` and `rebase`, without the builder's `switch`.
No shipped profile grants a toolchain: a repository grant adds a repository's own commands, such as `go test` for its builder, reviewer and rebaser.

The config digest a task and a Director session store is the JSON of the parsed config, so it changes for a copy that relies on shipped profiles; it is stored and never compared, so nothing strands.

## The shipped rebaser runs minor conflicts

A copy used to escalate every `minor_conflicts` rebase to `major_conflicts` unless it declared `rebaser`.
Every parsed Director config now has one, so a minor conflict runs a claude rebaser Artisan in every copy, with the adapter's default model unless the copy declares its own `[profile.rebaser]`.
The escalation path remains for a config without the profile, which only one built in memory can be; there is no setting to turn the rebaser off.

## Tasks bound to a defaulted digest

A task created on `default` or `bug` while role profiles were written into the definition bound that defaulted digest.
Resolving its workflow now yields the bare definition, whose digest differs, so it would strand with `ErrRevision`.
The bound-workflow resolution therefore also accepts the legacy digest, `workflow.ApplyProfileDefaults` over the bare effective definition with every role profile declared, and runs that defaulted definition, whose written profiles still win.
Every role is taken as declared because a task could bind a defaulted definition only with every role step defaulted: a role step left without a profile was refused.
`ApplyProfileDefaults` stays only for this comparison and never covers the rebase type, which it never defaulted.

## Retiring a workflow

`task list --workflow <name> --open` lists the open tasks bound to a workflow name, so a Director deletes `workflows/<name>.json` only once it prints none.
Nothing refuses the deletion; a task bound to a deleted file strands as before.
