# A task grant gives one task's Artisans extra allowed tools, for one attempt or until the task is done

The only way to give one task's Artisans an extra tool was to widen a whole profile in the copy's `config.toml`, so every task's attempts of that step got it until someone removed it by hand: d8's probe build needed `Bash(claude:*)`, `Bash(herdr:*)` and `Bash(go:*)` on `[profile.builder]`, and d28 planned a temporary researcher grant the same way.
The Guildmaster accepted the d12 plan: a **task grant**, a store record the Guildmaster approves, that adds `allowed_tools` rules to one task's attempts of one step and expires on its own.
This adds a fourth source to [D93](0093-shipped-default-grants.md)'s effective grant and keeps every [D89](0089-artisan-allow-rules.md) and D93 guarantee.

## The record

A task grant lives in a per-repository `task-grants` bucket, created lazily, so the store stays at schema 1.
It holds an id, `g1`, `g2`, … per task; the repository and task; a `step`, one step of the task's workflow or `all`; its `rules`; a non-blank `reason`; `until`, `attempt` (the default) or `done`; the `decision` that approves it, `grant-<id>`; `revoked`, the time and reason of a revoke once one is recorded; and `bound`, the attempts whose binding took it.
It grants `allowed_tools` rules and nothing else: no forge rights, merge powers, deny-rule removal or settings.

`task grant create` checks every rule in the write transaction: `config.CheckAllowedTool` and the duplicate check, as for profile and repository grants, and a task-grant refusal list, `store.CheckTaskGrantRule`.
The refusal list is any `Bash` rule whose command is `guildhall`, `gh` or `glab`, or `git` without a subcommand, with an option before its subcommand, with `push` or with a wildcard that could complete to `push`.
A wildcard command is refused when it could complete to any of them, so `Bash(g*)` and `Bash(gi*)` are refused, and a rule is judged by the command after `VAR=value` assignments and well-known wrappers, `env`, `command`, `exec`, `sudo`, `nice`, `nohup`, `timeout`, `xargs` and the like, with their options and numeric arguments, so `Bash(env gh:*)` is refused as `Bash(gh:*)` is.
Each of these would let an Artisan push, merge or answer its own decisions; `Bash(claude:*)`, `Bash(herdr:*)` and `Bash(go:*)` stay allowed.
The list is a speed bump against an obvious grant, not a boundary: a granted shell or interpreter, such as `Bash(sh:*)` or `Bash(go:*)`, can run anything, and the Guildmaster's approval of the quoted rules is what stands behind them.
A named step must be an Artisan step of the task's bound workflow whose profile runs the `claude` adapter, since the fake adapter refuses any grant; with `all`, only those steps take it, and `all` needs `until = done`, since "the next attempt" of every step has no clear meaning.
At most one grant per task and step may be pending or active at a time, `all` overlapping every step, and a second is refused naming the first.
So the creation mutation id `task-grant:<repo>:<task>:<id>` replays safely: the command reuses the id of an open grant with the same step, rules, reason and until, so a retry prints the original receipt, and anything else takes the next id and is refused.
A revoke is recorded once, with mutation `task-grant-revoke:<repo>:<task>:<id>`.

## Approval, and its state derived

Creating a grant opens, in the same transaction, a typed decision `grant-<id>` of the new kind `grant`, with no subject and the options `approve` and `reject`; its question quotes the step, rules, reason and expiry.
The grant takes effect only when the decision is answered `approve` through the existing `decide answer`.
Its state is derived, never written, as D92's park clearing is:

| state | when |
|---|---|
| `revoked` | a revoke is recorded |
| `pending` | the decision is unanswered |
| `rejected` | the decision is answered, but not `approve` |
| `expired` | the task is merged or closed, or, for `until = attempt`, its bound attempt has an outcome |
| `active` | otherwise |

"Only the Guildmaster grants" rests on the [D57](0057-director-conversation-first.md) model: the Director records the Guildmaster's answer with `decide answer`, as it does for a workflow approval.
The Director role says to open a grant only when the Guildmaster asks for one, never on its own judgement or from an Artisan's request, and to answer `approve` only with the Guildmaster's words.
That is conduct, not an enforced boundary: the Director session may run `decide answer`, so nothing stops a Director that ignores its role.
A dedicated approve command the session's settings deny would be stronger, and is still only a command-policy boundary; the plan kept D57.

## Binding at launch

When the engine records an attempt's binding (`<step>-<n>-inputs`), it looks up the task's grant active for that step, records the attempt on the grant (`BindTaskGrant`) and then writes the grant's id and rules into the binding as `grant`.
The binding is written once and never replaced ([D36](0036-immutable-attempt-preparation.md)), so `<step>-<n>.inputs.json` shows exactly which task-grant rules the attempt was given, and a replay is unchanged.

- An `until = attempt` grant binds only the first attempt of its step bound after approval, and counts as active until that attempt has an outcome. A binding that already existed before approval, as when a cap refused its start, does not take it; the grant waits for the next bound attempt.
- An `until = done` grant binds every attempt of its step or steps bound while it is active.
- Rebaser and conflict-resolution attempts are bound by their own path and never take a task grant; only attempts the engine starts for workflow steps do.
- A step whose profile runs another adapter takes none; under test the mock agent stands in for `claude`.

## The wrapper reads it from the store

`guildhall claude-artisan` already knows the copy, repository, task and attempt.
At every run it opens the copy's `director.store` read-only, finds the attempt's binding document and checks the file against the SHA-256 the store holds, and gives the bound rules only while the store does not record the grant revoked.
The Director's Edit tool can write attempt directories, so a grant is never read from a plain file nobody verified.

- A resume ([D48](0048-artisan-resume.md)) of the same attempt keeps the grant, even after the attempt finished.
- A revoke takes effect at the next wrapper run of any attempt: a later attempt or a resume. It never reaches a Claude session already running; stop that attempt to cut it short.
- A store that cannot be opened or read, a binding that is missing or does not match its digest, and a binding naming a grant the store does not hold fail the attempt before Claude starts. It never silently runs with other rules.
- A copy with no store, an attempt with no binding and one run without `--profile`, such as a hand-started tab, are unchanged; a profile run over a copy with no store says on stderr that no task grant is read.

`claude.WithTaskGrant` adds the rules as a fourth source, `task`, last, so a task grant only widens and a rule already granted keeps its earlier source.
They reach the session through `claude-settings.json` as every rule does; no rule list travels on argv or env, and nothing rewrites `config.toml`.
The fake adapter, the `director start` preflight and skill-policy validation pass no task context and never resolve a task grant.

## Visibility

`director status` has a `grants` section beside `parked`, listing every active grant: repository, task, grant id, step, rules, reason, `until`, the attempts bound so far and when its approval was recorded.
A grant leaves it when it expires or is revoked, with no write; a pending one shows in `needs_me` as its open decision, and `task grant list` shows every state.

## What does not change

The profile, shipped-default and repository grant order and dedup, and the preflight's `artisans` line.
Rules never come from `managed.toml`, a repository's own files or a shared file; Guildhall never writes `config.toml`; there is no `--allow-tool`; the settings file stays outside model-writable directories; deny rules still win.
The Director session's settings and deny list are unchanged.
`decide answer` never advances or launches a task, and approving a grant launches nothing.
The store stays at schema 1, `allowed_tools = []` still differs from an unset list, and the fake adapter still refuses any non-empty grant.
