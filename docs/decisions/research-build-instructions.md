# Research and build accept instructions

`research` and `build` accept an optional `instructions` string, as `review`, `document-review` and `guildmaster-approval` already do.
This is a registry change only: the renderer already renders any step's `instructions` into its prompt, and neither kind is guarded, so the setting adds guidance without weakening anything the workflow-edit authority checks.
`research` still refuses `skills`, as [research accepts a profile](research-profile.md) decided.

The shipped `bug` workflow uses them: research reproduces the defect and identifies its cause, or records that it could not, and build writes a regression test first, shows it failing before the fix and passing after it, and names it in the commit.
The bug build does not use `guildhall-contract-tdd`, because that skill is contract TDD against a spec and its checks run only on Go; the plain instruction says "regression test first" in any language.
