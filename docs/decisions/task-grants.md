---
status: accepted
date: 2026-10-30
topic: grants
tasks: d3, d12, d50
amends: claude-adapter-wrapper.md
amended-by: copy-discovery.md, role-profiles-outside-the-digest.md
formerly: D89, 0089-artisan-allow-rules.md, D93, 0093-shipped-default-grants.md
---

# Claude Artisans get extra tools only from the release and their Director: a shipped default, profile and repository grants, and an approved task grant

## Context

A Claude Artisan runs print mode under `acceptEdits` ([the wrapper record](claude-adapter-wrapper.md)), which allows neither running a repository's checks nor committing, and a print-mode session has no one to ask.
Widening it was first left to managed settings, which a standalone Director does not have; a d3 build then failed because its builder was denied `git fetch` and `git rebase` while rebasing; and d8's probe build and d28's researcher needed tools for one task only, which meant widening a whole profile until someone removed it by hand.

## Decision

A session's **effective grant** is resolved in one place, `grant.EffectiveGrant` and `grant.WithTaskGrant` ([`internal/adapter/grant`](../contracts/adapter-grant.md)), in this order, a repeated rule keeping its first source ([Artisan permissions](../adapters.md#artisan-permissions)):

1. the profile's `allowed_tools` in the Director copy's `config.toml` when set, even to `[]`; otherwise the **shipped default grant** of a `claude` profile with a well-known role name: read-only git for every role, plus commit and rebase git for the builder, the web tools for the researcher and `guildhall review check` for the reviewer;
2. the task repository's grant, `[repository.profiles.<name>]`;
3. an approved **task grant** recorded in the attempt's binding ([task grants](../tasks.md#task-grants)).

Every claude profile also gets the read-only `guildhall ci log` rule of [failed job logs](failed-job-logs.md), before any task grant.

The rules belong to the Director: they come only from the Guildhall release and the copy that `--copy` or copy discovery selects, never from `managed.toml`, a repository's own files or a shared file, and Guildhall never writes `config.toml`.
They travel by reference only (copy, profile, repository, attempt); the wrapper writes them as `permissions.allow` in a settings file in the attempt directory, outside every directory Claude may write.
They widen Claude Code's own settings and never narrow them; deny rules elsewhere still win.
`config.Parse` refuses malformed and duplicate rules and any rule allowing every shell command, so a grant always names its commands.
Only the claude adapter grants; the fake refuses a non-empty grant, and the `director start` preflight shows each profile's grant by source and refuses one it cannot honour.

A task grant is a store record that adds rules to one task's attempts of one step, or of all its claude steps, until its next attempt has an outcome or until the task is done.
A grant on a step covers that step in every slice of a multi-PR task, `<step>--s<k>`: until attempt, it binds the first attempt of the step or any of its slices bound after approval and expires when that attempt has an outcome; until done, it binds every attempt of every slice. A grant names the step, never a slice id.
It takes effect only once its decision is answered with its approving option: its typed decision `grant-<id>` answered `approve`, or, for a grant the Director proposed as one option of a question ([`decide open`](../decisions-cli.md#a-question-that-proposes-a-grant)), that question answered with that option; another option or free text rejects it. Its state is derived, never written, as [parking](park-attempt.md)'s clearing is.
A proposed grant is recorded pending with its question, in one transaction and after every check a created grant passes, with the question as its decision; the question's text names the exact step, rules, until and reason, so the Guildmaster approves the grant they read, once, with their own answer.
A grant whose rules differ from the proposal is an ordinary create, with its own `grant-<id>` decision.
The engine writes it into the attempt's immutable binding, and the wrapper reads it back from the store read-only at every run, checking the binding's digest, and fails the attempt before Claude starts if it cannot.
A task grant never allows `guildhall`, `gh`, `glab`, bare `git` or `git push`, directly, by wildcard or behind a wrapper such as `env`.

The Director's session may not edit its `config.toml` and is denied direct calls to `claude-artisan`, `fake-artisan`, `fake-director` and `artisan start`, so it cannot widen a grant through launch plumbing.

## Alternatives rejected

- Rules on the command line (`--allow-tool`, `--allowedTools`): Claude Code splits them on commas and spaces, a Windows shim re-quotes them, and any argv could widen a session.
- A Steward-distributed or machine-wide grant: two Directors on one machine need different sets; a managed grant is later work, if wanted.
- Empty by default: it failed d3's rebase, so well-known profiles ship a default.
- `git checkout` or `git push` in a default: checkout can discard work and `git switch` covers branch moves; the publish stage pushes.
- Widening a profile for one task's need: every task's attempts get it until removed by hand.
- A dedicated approve command the session's settings deny: still only a command-policy boundary, so approval stays with `decide answer` under the [Director conversation model](director-conversation-first.md).

## Consequences

This is a command-policy boundary, not an OS sandbox: processes of the same OS user are not isolated, and the wrappers trust the copy and profile reference they are given.
"Only the Guildmaster grants" a task grant is the Director's conduct under its role, not enforced, and the task-grant refusal list is a speed bump: a granted shell or interpreter can run anything.
A revoke reaches the next wrapper run, never a running session; a resume keeps the grant.
Copies whose well-known claude profiles set no list are widened by the default; `git switch` lets a builder commit on another branch, which build intake refuses.
The live permission tests have not been run against a real session.

## History

- 2026-10-23, D89 (0089-artisan-allow-rules.md): each Director grants its Claude Artisans narrow rules per profile through `allowed_tools`, empty by default, with a suggested Go set; this superseded the wrapper record's assignment of wider permissions to managed settings, so a standalone Director could grant them before managed mode existed.
  It chose a settings file over `--allowedTools` and refused `--allow-tool`, and set the Director session's denies on launch plumbing.
  The copy was then chosen by `--copy` or `GUILDHALL_COPY`; [copy discovery](copy-discovery.md) later replaced the variable.
- 2026-10-29, D93 (0093-shipped-default-grants.md): after d3's builder was denied the git it needed to rebase, `builder`, `planner`, `reviewer` and `researcher` claude profiles that set no list got a shipped default, and a repository could add rules for its own tasks; the suggested Go grant became `go test` and `go vet`.
  An explicit list still replaces the default, so a copy can tighten. d50 added `guildhall review check` to the reviewer's default. [Shipped role profiles](role-profiles-outside-the-digest.md) later added `git ls-files` and a `rebaser` grant.
- 2026-10-30, task-grants.md (d12): a task grant, approved through a typed decision, adds a fourth source for one task's attempts and expires on its own; `all` needs `until = done`, at most one grant per task and step is open at a time, rebaser and conflict-resolution attempts never take one, and `director status` lists active grants.
- 2026-11-13, d243: a grant on a step now covers that step in every slice, `<step>--s<k>`; before, it matched the step id exactly, so slice attempts after the first never took it (d222 `build--s3`, d161 `build--s3-2`). `grant create` still refuses a slice id, now naming its step.
- 2026-11-19, d262: a Director question may propose an exact grant as one of its options, recorded pending with the question as its decision and `approving_option`, so the Guildmaster's answer choosing that option approves it and `grant-<id>` is not asked as well; before, d222's q3 `grant-fetch` was answered and its grant's `grant-g2` asked again. Grants recorded before keep their meaning: an empty `approving_option` is `approve`.
  Considered: matching a later identical `create` to the answered proposal, and creating the grant inside the answer; recording it at open time runs the checks when the Director proposes and means an answer never fails on a grant conflict. One grant per question keeps one pending grant per step, and such a question, like any other, does not hold its task.
  An identical `task grant create` while the proposed grant is pending or active is refused with `ErrExists` naming the question and its option, rather than reusing the proposal's id as d262's spec said (recorded by d272): the proposal was recorded under the question's `decision-open` mutation, so there is no `task-grant` receipt to replay, and the approval is the Guildmaster's answer.
