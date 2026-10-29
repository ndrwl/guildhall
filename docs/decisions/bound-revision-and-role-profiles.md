# Tasks resolve their bound revision, and Artisan steps default to role profiles

A task's workflow is resolved by its bound revision's name at every advance, `step done` and `step finish`, not by the repository's current `workflow` setting.
The name resolves as at `task new`: the shipped definition of that name, otherwise `workflows/<name>.json`, then the repository's current overrides and profile defaults, and the digest must still be the bound one.
Before this, changing a repository's `workflow` setting stranded every open task with `ErrRevision`, so a repository could never move workflows with tasks in flight.
A task still strands, naming its bound workflow, when its file, the overrides or the profiles it was defaulted from change, because the store keeps only the digest.

When the effective workflow is resolved, after `ApplyOverrides` and before `Validate`, an Artisan step of type `research`, `plan`, `build` or `review` with no `profile` names `researcher`, `planner`, `builder` or `reviewer` when the config declares that profile.
Profiles keep their role names rather than step-type names, so Claude skill policy (`claude_skills_profile_<name>`) and `quality_judge_profiles` are unchanged.
Applying the default into the effective definition puts it in the digest, so the workflow-edit authority still sees a defaulted reviewer's profile as a weakening that needs the Guildmaster's approval.
Such a step was refused before, so no existing task's digest changes.
`refute` and `document-review` get no default because no shipped workflow uses them.
