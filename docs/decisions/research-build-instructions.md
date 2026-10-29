# Research and build accept instructions

`research` and `build` accept an optional `instructions` string, as `review`, `document-review` and `guildmaster-approval` already do.
This is a registry change only: the renderer already renders any step's `instructions` into its prompt, and neither kind is guarded, so the setting adds guidance without weakening anything the workflow-edit authority checks.
`research` still refuses `skills`, as [research accepts a profile](research-profile.md) decided.
`plan` accepts `instructions` too, for the same reasons: it is not guarded, and the shipped `bug` workflow needs them once it has no acceptance step ([decision](0095-bug-workflow-without-acceptance.md)).

The shipped `bug` workflow uses them: research reproduces the defect from the task title and finds its cause, or fails with what it tried and found when it cannot reproduce it or the behaviour is not a defect; plan takes the recommended answer to an ordinary open question and records it, or fails with a question that needs the Guildmaster; and build writes a regression test first, shows it failing before the fix and passing after it, and names it in the commit.
The bug build does not use `guildhall-contract-tdd`, because that skill is contract TDD against a spec and its checks run only on Go; the plain instruction says "regression test first" in any language.
