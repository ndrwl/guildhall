# Configuration and workflows

Each Guildhall copy has a local TOML config file, and enrolled rollout copies also have a separate Steward-owned `managed.toml`; each Director has JSON workflow definitions.
Pure parsing APIs in `internal/config` and `internal/workflow` run nothing and grant no authority.
`config.Load` reads the config and its sibling managed file when present, while `config.ParseCombined` validates supplied bytes without file access.
Legacy `config.Parse` still accepts inline managed settings, and `config.Load` falls back to it when no sibling `managed.toml` exists.
When that sibling exists, `config.Load` uses `config.ParseCombined`, which refuses inline managed settings with migration guidance.
Only a sibling that does not exist falls back: a sibling that exists but cannot be read, such as a directory, or that `config.ParseCombined` refuses, such as one with a checksum mismatch, is an error rather than a silent return to the inline layout.
A config file that cannot be read is an error.
No credentials belong in either; sign-in stays in each runtime's own store.

## Config file

The config is versioned by `schema_version`.
A config with a newer `schema_version` than this Guildhall supports is refused rather than guessed at, and unknown keys are refused.
See [`examples/director.toml`](../examples/director.toml) for a Director's local config and [`examples/managed.toml`](../examples/managed.toml) for an initially empty managed file.
The managed file's first line binds the exact remaining bytes by SHA-256; use `config.EncodeManaged` to generate it, rather than editing its payload without regenerating the checksum.

| Key | Meaning |
|---|---|
| `schema_version` | Required; currently `1`. |
| `mode` | `steward` or `director`. |
| `name` | The Director's name; required in director mode. |
| `[private_config] location` | Steward only: where the private config repository lives, as a path or remote; never a credential. |
| `[managed.<key>]` | In the separate managed file for [Steward rollouts](steward.md): `value`, and `overridable` (default `false`). |
| `[local]` | Owned by the copy: `<key> = "value"`. |
| `[[repository]]` | Director only: one entry per repository the Director owns. |
| `[profile.<name>]` | Director only: a named Artisan [profile](#profiles). |

Keys whose names look like credentials, such as `token` or `password`, are refused in both sections.

### Precedence

A local value replaces a managed value only when that managed key is marked `overridable = true`.
A local value for a managed key that is not overridable is refused, so a managed rule is never silently overridden.
A local key with no managed counterpart is the copy's own setting.

### Repositories

| Key | Meaning |
|---|---|
| `id` | Unique lowercase identifier. |
| `path` | Absolute path of the repository on the owner machine. |
| `remote` | Git remote; web URLs must not embed user info and no URL may embed a password. |
| `owner_machine` | The Director's one owner machine; every repository of a Director names the same one. |
| `merge_policy` | `approval` (default) or `auto`. |
| `workflow` | Name of the Director's workflow definition; defaults to `default`. |
| `[repository.workflow_overrides.<step-id>]` | Chooses that step's `actor` for this repository only. |

Repository paths accept a POSIX path starting with `/` or a rooted Windows drive-letter path on any parsing host; backslash-prefixed UNC paths are refused.
Two repositories may not use the same path after dot segments are cleaned; drive paths also normalize separators and drive-letter case.
Other letter case is compared exactly.

`approval` means the Director asks the Guildmaster before every merge.
`auto` means a merge happens only when checks pass, the independent review is complete for the exact head and the work is within accepted scope; otherwise the Director asks.
No workflow can change a repository's merge policy.

### Profiles

A profile chooses who runs an Artisan stage whose `profile` setting names it.
Names are lowercase identifiers, so three review stages can name `reviewer-opus`, `reviewer-opus-b` and `reviewer-astra` and no model name appears in a repository's workflow.

| Key | Meaning |
|---|---|
| `adapter` | Required: the runtime adapter, such as `claude`. |
| `model`, `effort` | Passed to the adapter as given; empty means the adapter's default. |
| `role` | A role prompt replacing the release's Artisan role: a relative slash-separated path inside the config file's directory. |

A profile names no credential.
`config.CheckProfiles` refuses a workflow whose steps name a profile the config does not declare, listing every such step.

## Workflow definitions

A workflow defines the steps of one task as an ordered list of built-in step kinds.
It does not replace the task dependency graph: independent tasks still run in parallel, each with its own workflow.
JSON names built-in kinds and gives their settings; it cannot contain commands, code, expressions or new step kinds.
The shipped default is [`internal/workflow/default.json`](../internal/workflow/default.json).
[`examples/workflows/human-review.json`](../examples/workflows/human-review.json) illustrates Guildmaster review of the plan and of every change, with reviewer instructions, and omits the default's research step.

```json
{
  "schema": "guildhall.workflow/1",
  "name": "small",
  "steps": [
    {"id": "plan", "type": "plan", "version": 1},
    {"id": "accept", "type": "guildmaster-approval", "version": 1,
     "inputs": {"spec": ["plan.spec"]}},
    {"id": "build", "type": "build", "version": 1, "inputs": {"spec": ["accept.spec"]}},
    {"id": "review", "type": "review", "version": 1, "settings": {"actor": "artisan"},
     "inputs": {"spec": ["accept.spec"], "head": ["build.head"]},
     "on": {"changes_requested": {"goto": "build", "max": 3}}},
    {"id": "publish", "type": "publish", "version": 1, "inputs": {"head": ["build.head"]}},
    {"id": "deliver", "type": "deliver", "version": 1,
     "inputs": {"head": ["publish.head"], "review": ["review.outcome"]}}
  ]
}
```

| Field | Meaning |
|---|---|
| `schema` | `guildhall.workflow/<version>`; a newer version is refused. |
| `name`, `steps[].id` | Lowercase identifiers; step ids are unique. |
| `steps[].type`, `steps[].version` | A built-in kind and its version, both required, so a task never silently changes kind version. |
| `steps[].settings` | Only the settings the kind declares, with their declared JSON types, and `actor` where the kind allows more than one actor. |
| `steps[].inputs` | Input port to an array of `"step.output"` references, including for single-input ports. |
| `steps[].on` | Outcome to `{"goto": <step-id>, "max": <rounds>}`. |

Call `workflow.Parse` to decode a definition, then `workflow.Validate` before using it; parsing alone does not validate wiring.
Parsing stops at the first decoding error, while validation reports the semantic problems it finds together.
JSON field names must match their declared spelling exactly, including case.
Together, parsing and validation refuse unknown fields and duplicate keys, unknown kinds or versions, unknown or mistyped settings, missing required settings, a missing or disallowed `actor`, unwired required inputs, inputs that reference unknown steps or outputs, and inputs whose types do not match.
An input can only come from an earlier step, which makes each step's readiness explicit and rules out dependency cycles.
A rule can only send the task back to the same or an earlier step, and `max` must be between 1 and 10, so every loop is bounded.

### Built-in step kinds

The authoritative [built-in registry](../internal/workflow/registry.go), exposed through `workflow.Kinds`, declares each kind's version, typed ports, cardinality, settings and loop outcomes.

The registry declares which kinds accept `profile`, a [profile](#profiles) name, and `skills`, a list of distinct skill names rendered for the Artisan ([rendering](rendering.md#skills)); reviews keep them when swapping actors, and they are unused while the Guildmaster reviews.
For the grill step's skills support and constraints, see the [workflow contract](contracts/workflow.md).

Each kind declares who may fulfil it, and `Step.Actor` reports who fulfils a step:

| Actor | Meaning |
|---|---|
| `artisan` | A fresh Artisan session is launched for the step. |
| `guildmaster` | A durable pause that waits for the Guildmaster without model calls. |
| `director` | The step runs inside the Director's own session and ends with a recorded document. |
| `engine` | Guildhall runs the step itself, with no model. |

| Kind | Actors | Produces |
|---|---|---|
| `grill` | `director` or `artisan` | notes |
| `research`, `plan`, `build` | `artisan` | notes, a spec document, a code head |
| `document-review` | `artisan` or `guildmaster` | a review outcome for a document, such as the plan's spec |
| `guildmaster-approval` | `guildmaster` | the accepted spec; may take document-review outcomes as `reviews` |
| `review` | `artisan` or `guildmaster` | a review outcome for a code head against the accepted spec |
| `publish` | `engine` | the same head, pushed to the repository's remote branch with its pull request opened or updated |
| `validate`, `deliver` | `engine` | a validation result, the landed change |
| `handoff-wait` | `guildmaster` | a handoff document |

The director actor on `grill` is part of the accepted workflow design's stage table (design report section 4.1: grill produces notes, with actor director, a conversation with the Guildmaster, or artisan) and does not conflict with R1.5, because a grill is the Director clarifying requirements with the Guildmaster, not doing the work.
A kind with more than one actor requires the `actor` setting; a kind with one actor refuses it.
`publish` comes before `validate` so forge checks have a pushed head to read; it never merges.
`publish` and `deliver` have no settings: the repository's configuration and merge policy decide where work goes and how it lands.

### Choosing actors per repository

A repository changes who fulfils a step with `workflow_overrides` in the config, for example the Guildmaster instead of an Artisan for review:

```toml
[repository.workflow_overrides.review]
actor = "guildmaster"
```

Only a step whose kind allows more than one actor can be overridden, and only with an actor its kind allows, so the kind, wiring and other settings stay the same.
`workflow.ApplyOverrides` returns a copy with the requested `actor` settings replaced; callers must run `workflow.Validate` on the result before use.
The resulting definition's digest changes when its canonical content changes.

### Revisions

A workflow's digest is the sha256 of the parsed definition's canonical JSON encoding, written `sha256:<hex>`.
Reformatting the file or changing an omitted optional field to its empty value does not change the digest; changes retained in the canonical encoding do.
`workflow.Revision` supplies the name and digest pair used by [task acceptance](tasks.md#the-bound-workflow).

### Review outcome

Every `review` and `document-review` step produces the same outcome, schema `guildhall.review-outcome/1`, whoever its actor.

| Field | Meaning |
|---|---|
| `reviewer` | `human` or `automated`. |
| `verdict` | `approved`, `changes_requested` or `rejected`; the latter two need at least one finding. |
| `findings[]` | `summary` and `id` (required), optional `rule`, `file` and `line`. An `id` is the reviewer's stable name for the finding across rounds, used once per outcome; `rule` cites the rule it breaks, and a finding without one is a suggestion that never blocks. |
| `dispositions[]` | What became of each finding of the step's previous outcome: `finding` (its id) and `state`, one of `resolved`, `still-open` (listed again in `findings` under the same id) or `withdrawn` (not listed). |
| `revisions.subject` | What was reviewed: exactly one of `code_head`, the full commit id, or `document_digest`, the document's `sha256:<hex>` digest. |
| `revisions.spec_digest` | `sha256:<hex>` digest of the accepted specification; required for a code head, optional for a document. |
| `revisions.standards_revision` | Central standards revision reviewed against. |

An outcome blocks the task only when its verdict is `changes_requested` and at least one finding cites a rule.
An outcome counts only for the exact subject, specification digest and standards revision it names, so a document review never counts for a code head or the reverse.

### Workflow-edit authority

Directors may propose workflow changes.
Guildmaster approval is required before applying changes that affect review requirements, required checks or approval steps.
No blanket delegated authority or automatic self-approval is granted.
A task keeps its pinned workflow revision when a workflow is edited.
Approvals are recorded per Director, against a definition's exact digest:

```sh
guildhall workflow approve --store director.store --file workflows/human-review.json --words-file -
guildhall workflow approvals --store director.store --json
```

`approve` parses and validates the file, computes its digest and records the Guildmaster's words from the file or standard input, exactly as given; it prints the [store receipt](store.md#mutations-and-receipts).
A refused definition, empty words or a missing store write nothing.
Repeating an approval prints the original receipt; different words for an approved digest are refused.
An edited definition has a new digest and needs its own approval.
The definitions shipped with this release, such as the default workflow, count as approved by the release: `approvals` lists them with source `release` before the recorded approvals (source `guildmaster`).
Explicitly approving shipped content records the Guildmaster's words like any other approval; that revision then appears with both sources.
Commands exit 0 on success, 1 when the definition, words or store refuse or fail, and 2 on a usage error; they never create a store.
Enforcing this policy when a task is created belongs to a later execution layer, not this foundation package.
Parsing and validation accept or refuse a definition only; they never activate it.
