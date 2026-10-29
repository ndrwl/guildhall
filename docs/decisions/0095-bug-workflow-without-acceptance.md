# D95. The shipped bug workflow has no grill and no acceptance

The Guildmaster asked for a bug task to run without a Guildmaster stage before merge: the shipped `bug` workflow drops its `grill` and `accept` steps and runs research, plan, build, review, publish, validate and deliver.
The shipped `default` workflow and a copy's own workflows, such as guildhall's `dogfood`, keep both steps; `default.json` is unchanged byte for byte, so its digest, tasks and approvals are unaffected.

## Decisions

- **The Guildmaster chose this, and it means a bug fix can merge with no human reading its spec.**
  A bug task merges under the repository's [merge requirements](../configuration.md#merge-requirements) like every other task.
  Where they leave `guildmaster_approval` off, as guildhall's do, a bug fix is planned, built, reviewed by an independent Artisan, validated and merged without the Guildmaster or any other human reading its spec.
  The review floor (an independent review outcome and a validation result before deliver) still holds.
- **Build, review and rebase take a document or an accepted spec as their spec.**
  Their `spec` input is typed `accepted-spec` and lists `document` in `Port.Also`; `Validate` accepts either, so `build.spec` can be wired to `plan.spec`.
  No other port lists extra types, and every other mismatch is refused with the same error as before.
  The type check therefore no longer proves that a build's spec was accepted; that is the source's job, below, and the workflow-edit authority's.
- **The spec gate follows the source, not the port.**
  The engine checks a spec against its approval decision when the output it is wired to is an `accepted-spec`, that is, when it comes from `guildmaster-approval`, exactly as before; a `plan.spec` passes through ungated.
  Review outcomes, human review revisions, the rebaser, narrowed review and deliver's merge authorization (`specDigest`, `sameSpec`) still bind to the digest of the spec the build was given, since they read the input, not its type; after a Director send-back to plan, build reads the new `plan.spec`.
- **Removing acceptance from an authorized baseline is still a weakening.**
  `guildmaster-approval` stays guarded, so a custom workflow derived from the default or `dogfood` that drops `accept` is still refused at task creation as a weakening (`authority.go`) until the Guildmaster approves it; the shipped `bug` counts as approved by the release, as before.
- **Bug Artisans work from the task title.**
  With no grill, nothing else states the defect, so every Artisan prompt now shows the task title after its header, fenced as text so it cannot open a prompt section; the title is not in the manifest's `Identity`, so `ManifestSchema` does not change.
  This adds the title to the default workflow's prompts too: only the workflow definitions are unchanged outside `bug`.
- **Plan accepts instructions, and questions come back as a failed attempt.**
  `plan` accepts `instructions` ([decision](research-build-instructions.md)); the bug plan takes the recommended answer to an ordinary open question and records it as a decision, and fails with the question in its summary when it needs the Guildmaster.
  The bug research fails with what it tried and found when it cannot reproduce the defect or the behaviour is not a defect.
  The `attempt-failed` message is unchanged; the Director's tasks skill says to read the attempt summary with `task show` and bring it to the Guildmaster before any `step retry`.
- **No new step kind.**
  An engine-run acceptance kind, or an engine actor on `guildmaster-approval`, would record a self-approval that [workflow-edit authority](../configuration.md#workflow-edit-authority) rules out.

## Rollout

The shipped `bug` digest changes, so an approval of the old resolved bug revision stops applying and an in-flight bug task would strand at its next advance.
A copy whose role profiles change the bug workflow's review step re-approves the new revision once it runs a Guildhall with this change: `guildhall workflow approve --config <config> --repo <id> --kind bug --words-file -` with the Guildmaster's words; approving before the release lands approves the old digest.
