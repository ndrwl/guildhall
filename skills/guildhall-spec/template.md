# Spec template

Write the result in this order and keep every heading.
Write "None" under a heading that has nothing, with the reason.

```markdown
## Problem

The problem the task solves, from the point of view of whoever meets it now.

## Solution

What changes for them once the work is done, from the same point of view.

## Contracts

One entry per boundary the change touches or adds:

- `<module or interface>`: what it promises after the change.
  - Types and signatures: the types, functions and configuration shapes that change, by name.
  - Errors: each error a caller can see and what it means.
  - Ownership: who creates, holds and releases each resource.
  - Concurrency: what may run at once and what is serialised.
  - Idempotency: what happens when the operation is repeated.
  - Identity: how each thing is named and told apart.
  - Recovery: what state a failure leaves and how a retry resumes.
  - Platforms: differences across Windows, Linux and macOS or POSIX shells and PowerShell, where the boundary crosses them.

## Contract tests

- Suites: which contract tests are added or changed, at which contract.
- Real and fake: which suites run against both the real implementation and its test double.
- Red run: what the first run shows failing before the implementation exists.
- Prior art: existing tests of the same kind to follow.
- Model calls: none; tests start no model and need no credentials.

## Acceptance criteria

1. <a concrete behaviour a reviewer can check on its own>
2. <another>

## Out of scope

- <adjacent work this spec does not do>

## Size

One pull request, because <reason>.
Or: splits into <n> tasks, in order: <task 1>, <task 2>, each with its own acceptance criteria.

## Open questions

**Q1. <question title>**
<question body>
Recommended: <your answer, which this spec already follows>.
```

A good acceptance criterion names an observable result: "`Install` into a non-empty directory returns `ErrNotEmpty` and writes nothing".
A poor one cannot be checked: "install works correctly".
