# `internal/policies`

## Contract

A repository's policies are the Guildmaster's standing rules for that repository, kept in the repository itself and shown to the Director and Artisans as guidance; nothing reads their text to decide anything.
`Path` is the policy file, `.guildhall/policies.md` relative to the repository root, fixed and not configurable.
`Ref` is `refs/guildhall/policies`, the ref each repository's default branch is fetched into, so every reader reads the file at the same commit.
`MaxItemBytes` (1024) bounds one policy, as a note is bounded, and `MaxFileBytes` (16 KiB) bounds the whole file.
`Parse` returns the policies in order: each top-level list item, a line starting with `- `, is one policy written on one line, with surrounding spaces trimmed.
Headings, blank lines, paragraphs, a dash without a space and indented text outside an item are commentary and are not returned, and an empty file has no policies.
It refuses an indented line under an item, even after a blank line, a blank item or bare dash, a duplicate item, an item over `MaxItemBytes` or holding a carriage return, a file over `MaxFileBytes` and a file that is not valid UTF-8.
Each refusal names `Path`, and each refusal of an item names its line; a continuation also names the item's line, and a duplicate the line it repeats.
`Git` is the narrow seam `Load` reads through, one `Show` of one file at one revision, which `git.Program` implements.
`Load` reads the file from `Ref`, never from a task branch or a work tree, and returns a `Read` with one of four outcomes.
Before the first fetch, when `Ref` does not exist (`git.ErrNoRevision`), it is `NotRead`, which is not an error; another git failure is its `Err`.
An absent file is read, at its `Commit`, with no policies.
A file `Parse` refuses carries its `Commit` and the refusal as `Err`.
A file read cleanly carries its `Commit` and its `Policies`.

Rationale: [Repository policies](../configuration.md#repository-policies); no decision record covers this package.
Pure parsing, with one narrow seam over git.

## Tests

`policies_test.go`: `TestParse` covers each accepted shape and each refusal, checking the refusal names `Path` and the line; `TestOwnPolicyFileParses` checks this repository's own policy file parses with at least one policy; `TestLoad`, over a fake `Git`, checks the file is asked for at `Ref` and `Path` and each outcome of `Load`.
The consumers are tested where they use it.
`internal/cli/policies_test.go`: `TestPromptCarriesRepositoryPolicies`, `TestDirectorStatusWithoutPolicyFiles`, `TestDirectorStartShowsRepositoryPolicies`, `TestAdvanceRefreshesRepositoryPolicies` and `TestFeedAndStartupPolicyLines` cover the prompt section, the Director status, the startup file, the fetch into `Ref` before rendering and the startup and feed lines.
`internal/interp/policy_guard_test.go`: `TestPolicyChangeNeedsTheGuildmaster`, `TestNoPolicyChangeMergesByItself`, `TestRebasedPolicyChangeFollowsReapprove` and `TestRebuiltPolicyChangeCarriesOnlyTheGuildmaster` cover a head that changes `Path` needing the Guildmaster's approval to merge.
