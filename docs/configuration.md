# Configuration and workflows

Each Guildhall copy has one TOML config file, and each Director has JSON workflow definitions.
Both are parsed and validated by pure code in `internal/config` and `internal/workflow`, which read no other files, run nothing and grant no authority.
No credentials belong in either; sign-in stays in each runtime's own store.

## Config file

The config is versioned by `schema_version`.
A config with a newer `schema_version` than this Guildhall supports is refused rather than guessed at, and unknown keys are refused.
See [`examples/director.toml`](../examples/director.toml) for a complete Director config.

| Key | Meaning |
|---|---|
| `schema_version` | Required; currently `1`. |
| `mode` | `steward` or `director`. |
| `name` | The Director's name; required in director mode. |
| `[private_config] location` | Steward only: where the private config repository lives, as a path or remote; never a credential. |
| `[managed.<key>]` | Written only by Steward rollouts: `value`, and `overridable` (default `false`). |
| `[local]` | Owned by the copy: `<key> = "value"`. |
| `[[repository]]` | Director only: one entry per repository the Director owns. |

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
| `[repository.workflow_overrides.<step-id>]` | Replaces a review step's kind for this repository only: `type` and `version`. |

`approval` means the Director asks the Guildmaster before every merge.
`auto` means a merge happens only when checks pass, the independent review is complete for the exact head and the work is within accepted scope; otherwise the Director asks.
No workflow can change a repository's merge policy.

## Workflow definitions

A workflow defines the steps of one task as an ordered list of built-in step kinds.
It does not replace the task dependency graph: independent tasks still run in parallel, each with its own workflow.
JSON names built-in kinds and gives their settings; it cannot contain commands, code, expressions or new step kinds.
The shipped default is [`internal/workflow/default.json`](../internal/workflow/default.json), and [`examples/workflows/human-review.json`](../examples/workflows/human-review.json) shows the same workflow with human review.

```json
{
  "schema": "guildhall.workflow/1",
  "name": "small",
  "steps": [
    {"id": "plan", "type": "plan", "version": 1},
    {"id": "accept", "type": "guildmaster-approval", "version": 1,
     "inputs": {"spec": ["plan.spec"]}},
    {"id": "build", "type": "build", "version": 1, "inputs": {"spec": ["accept.spec"]}},
    {"id": "review", "type": "automated-review", "version": 1,
     "inputs": {"spec": ["accept.spec"], "head": ["build.head"]},
     "on": {"changes_requested": {"goto": "build", "max": 3}}},
    {"id": "deliver", "type": "deliver", "version": 1,
     "inputs": {"head": ["build.head"], "review": ["review.outcome"]}}
  ]
}
```

| Field | Meaning |
|---|---|
| `schema` | `guildhall.workflow/<version>`; a newer version is refused. |
| `name`, `steps[].id` | Lowercase identifiers; step ids are unique. |
| `steps[].type`, `steps[].version` | A built-in kind and its version, both required, so a task never silently changes kind version. |
| `steps[].settings` | Only the settings the kind declares, with their declared JSON types. |
| `steps[].inputs` | Input port to an array of `"step.output"` references, including for single-input ports. |
| `steps[].on` | Outcome to `{"goto": <step-id>, "max": <rounds>}`. |

A workflow is validated before any task may start it, and every problem found is reported together.
JSON field names must match their declared spelling exactly, including case.
Validation refuses unknown fields and duplicate keys, unknown kinds or versions, unknown or mistyped settings, missing required settings, unwired required inputs, inputs that reference unknown steps or outputs, and inputs whose types do not match.
An input can only come from an earlier step, which makes each step's readiness explicit and rules out dependency cycles.
A rule can only send the task back to the same or an earlier step, and `max` must be between 1 and 10, so every loop is bounded.

### Built-in step kinds

| Kind | Inputs | Outputs | Settings | Loop outcomes |
|---|---|---|---|---|
| `grill` | - | `notes` document | - | - |
| `research` | - | `notes` document | - | - |
| `plan` | `context` documents (optional, several) | `spec` document | - | - |
| `guildmaster-approval` | `spec` document | `spec` accepted-spec | `instructions` | `changes_requested` |
| `build` | `spec` accepted-spec | `head` code-head | - | - |
| `automated-review` | `spec` accepted-spec, `head` code-head | `outcome` review-outcome | `instructions` | `changes_requested` |
| `human-review` | `spec` accepted-spec, `head` code-head | `outcome` review-outcome | `instructions` | `changes_requested` |
| `validate` | `head` code-head | `result` validation-result | - | `failed` |
| `deliver` | `head` code-head, `review` review-outcomes (optional), `validation` (optional) | - | - | - |
| `handoff-wait` | - | `handoff` document | `from` (required) | - |

`guildmaster-approval`, `human-review` and `handoff-wait` are durable human pauses.
`deliver` has no settings: the repository's merge policy decides how work lands.

### Swapping review per repository

`automated-review` and `human-review` have the same inputs, outputs and outcomes, so either can replace the other.
A repository swaps them with `workflow_overrides` in the config, and only review steps can be overridden.
The override produces a new workflow content, which is validated and has its own digest.

### Revisions

A workflow's digest is the sha256 of its canonical JSON encoding, written `sha256:<hex>`, so reformatting the file does not change it but any content change does.
A task binds the name and digest of its workflow, after repository overrides, when it is accepted.

### Review outcome

Human and automated review produce the same outcome, schema `guildhall.review-outcome/1`.

| Field | Meaning |
|---|---|
| `reviewer` | `human` or `automated`. |
| `verdict` | `approved`, `changes_requested` or `rejected`; the latter two need at least one finding. |
| `findings[]` | `summary` (required), optional `file` and `line`. |
| `revisions.code_head` | Full commit id that was reviewed. |
| `revisions.spec_digest` | `sha256:<hex>` digest of the accepted specification. |
| `revisions.standards_revision` | Central standards revision reviewed against. |

An outcome counts only for the exact code head, specification digest and standards revision it names.

### Not decided here

Who may author or approve workflow edits, and any minimum review requirement, are open product questions.
Parsing and validation accept or refuse a definition only; they never activate it.
