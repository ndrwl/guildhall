# D93. Well-known Claude profiles get a shipped default grant, and a repository can add to it

A d3 build attempt failed because its builder was denied `git fetch`, `git rebase`, `git switch` and `git checkout` while rebasing a pull request: under [D89](0089-artisan-allow-rules.md) a profile's `allowed_tools` was empty by default, and the copy's builder list held only the suggested Go set.
The Guildmaster accepted that Guildhall should ship sensible defaults for the well-known profiles, with per-repository additions in the Director copy.
This amends D89's "empty by default" and keeps every other D89 guarantee.

## The shipped default grant

A `claude` profile named `builder`, `planner`, `reviewer` or `researcher` that does not set `allowed_tools` gets its **shipped default grant**, `claude.ShippedDefaultGrants`:

- each gets read-only git: `Bash(git <command>:*)` for `status`, `diff`, `log`, `show`, `rev-list` and `merge-base`;
- `builder` also gets `git add`, `git commit`, `git fetch`, `git rebase` and `git switch`;
- `researcher` also gets `WebSearch` and `WebFetch`.
- Amended by d50: `reviewer` also gets `Bash(guildhall review check:*)`, so it can check its review outcome as intake will before it finishes.

No default grants `git checkout`, which can discard work and which `git switch` covers for branch moves, or `git push`, which the publish stage does.
The build stage guide tells the builder to use `git switch` and that it may not push.
No default grants a toolchain command, since those differ by repository.
A profile of any other name, or of another adapter whatever its name, gets no default, so fake-run workflows keep working.

A copy profile that sets `allowed_tools` replaces the default, as before, so a copy with an explicit list keeps exactly that list and a copy can tighten; `allowed_tools = []` grants nothing and differs from an omitted key, a difference `config.Parse` takes from the decode metadata and the profile keeps through JSON.

## The repository grant

A `[[repository]]` entry in the copy's `config.toml` may add rules to a profile for that repository's tasks only, as `[repository.profiles.<name>] allowed_tools = [...]`.
`config.Parse` checks each rule as a profile's are, refuses a duplicate, a profile the copy does not declare and a profile whose adapter is not `claude`.
The suggested grant for a Go repository's builder, `claude.SuggestedGoAllowedTools`, is now `go test` and `go vet`; its git comes from the builder's default.

## The effective grant

`claude.EffectiveGrant` is the one resolver: the copy's list if set, else the shipped default, then the task repository's grant, with a repeated rule dropped silently, keeping its first source.
The wrapper, the fake adapter and the `director start` preflight all use it; the preflight's `artisans` line shows each profile's effective grant grouped by source and labels each repository's additions with its id, and notes a claude profile of another name with no list gets no shipped default.

The repository travels like the copy and profile, by reference only: `adapter.Spec.Repository` from the attempt's manifest or `artisan start --repo`, forwarded to `guildhall claude-artisan` as `--repo`.
No rule list travels on argv.

## What does not change

Grants still come only from the Guildhall release and the Director copy's `config.toml`, never from `managed.toml`, a repository's own files or any shared file, and Guildhall never rewrites a copy's `config.toml`.
There is still no `--allow-tool`, the settings file stays outside model-writable directories, deny rules elsewhere still win, and the Director session's denies on direct `claude-artisan` and `artisan start` calls are unchanged, `--repo` being an argument to the same denied commands.

Copies whose claude `builder`, `planner`, `reviewer` or `researcher` sets no list are widened by their default; the preflight shows it, and the Guildmaster trims a copy by hand.
`git switch` lets a builder commit on another branch; the stage guide tells it not to, and build intake refuses a head that is not the task branch's tip.
