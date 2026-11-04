# `internal/adapter/grant`

## Contract

The one owner of an Artisan profile's effective grant ([Artisan permissions](../adapters.md#artisan-permissions), [D93](../decisions/task-grants.md)).
It imports `config`, `admit` and the leaf `internal/adapter` and no runtime adapter, so `claude`, `codex` and `fake` each read it and none imports another.
`EffectiveGrant` resolves the grant the config gives a profile for a task of a repository, in order: the profile's own `allowed_tools` when set, even empty (source `copy`), or else, when its runtime grants tools (`adapter.GrantsTools`, only `claude`), its `ShippedDefaultGrants` entry (source `default`); then the repository's grant for the profile (source `repository`); then, for a profile whose runtime grants tools, `GuildhallRules` (source `guildhall`): `CILogRule`, `ResultRules` and `MessagesRule`, in that order.
A repeated rule is dropped, keeping its first source; an empty or unlisted repository adds nothing; an undeclared profile is an error; a profile of a runtime that grants no tools, such as a `fake` `builder`, gets no shipped default and no Guildhall rules ([failed job logs](../decisions/failed-job-logs.md)).
`ShippedDefaultGrants` has an entry for every `config.ShippedProfiles` name: read-only git, `ls-files` included, for each, the builder's commit, rebase and switch git, the rebaser's the same without `switch`, the reviewer's `guildhall review check` and the researcher's web tools ([decision](../decisions/role-profiles-outside-the-digest.md)); `SuggestedGoAllowedTools` is the documented repository grant for a Go builder.
`WithTaskGrant` adds a task grant's rules last, as a fifth source, `task`, so a task grant only widens and a rule already granted keeps its source; it does not change its input. `GrantRules` lists a grant's rules in order.
`ProfileTools` gives the rules of the effective grant the Director copy at a directory gives a profile for a repository, loading `<copy>/config.toml` through `admit.Admit`, so an attempt never runs with a `managed.toml` the copy has not authorized; with no copy or no profile it loads nothing and grants nothing.
It is the one reference resolver every adapter uses: `claude` materialises the rules, and `codex` and `fake` refuse a profile with any rule.

## Tests

`TestEffectiveGrant` and `TestFakeBuilderGetsNoDefault` check the resolver, including the `guildhall` rules on every claude profile with or without a copy list and none on a `fake` or `codex` profile, no `gh` or `curl` rule, and the shipped `rebaser` plus a repository grant giving `go test` with no copy section.
`TestShippedProfilesHaveShippedGrants` checks every shipped profile has a grant; `TestShippedClaudeRoleDefaults` checks the effective grants of the shipped profiles `examples/director.toml` relies on; `TestWithTaskGrant` checks the task source and that the input is unchanged; `TestGrantsTools` checks that only `claude` grants tools.
They moved here from `internal/adapter/claude` with the code; the `ProfileTools` admission is pinned over the Claude wrapper by `TestRunAdmitsTheCopysManagedRevision`.
