# Review standard

Every pull request is reviewed against this standard, the requirements in `docs/requirements.md`, the decisions in `docs/decisions.md` and `CONTRIBUTING.md`.
Human and automated reviewers apply the same rules.
For an interpreter code-review attempt, use the head input and the prompt's recorded `Review base` commit to identify the range, not the current default branch.
A `resolution` input narrows a rebase-only round to its resolution diff; the recorded base still describes the current range.
An unavailable recorded base is a blocker to report, not permission to guess a range.

The [quality rubric](quality.md) is the scored and trend layer above these rules: it records how good a delivered change was, maps each rule below to its criteria, and never changes what blocks a merge.

## Findings

Each finding names the file and line, the rule it breaks (a requirement id such as `R1.27` or a rule id below such as `S4`), and a concrete failure: the input or state and the wrong result.
A finding without a cited rule is a suggestion, not a blocker.
The verdict is approved, changes requested or rejected, and applies to the exact head commit reviewed.

## Rules

- **S1 Specification.**
  The change implements what its milestone and the cited requirements ask, and nothing they do not.
  Behaviour that contradicts a requirement is a blocker even when tests pass.
- **S2 Tests first.**
  New behaviour comes with a test that failed before the change, and the pull request description shows the red and the green runs.
  Tests are deterministic, with no wall-clock races; process doubles and integration tests follow [the contract testing policy](contracts.md#test-doubles-and-shared-contract-tests), including its network constraints and [shell testing rules](contracts.md#platforms-and-shells).
- **S3 Three platforms.**
  Build, vet, tests, formatting and lint pass on Linux, Windows and macOS in CI.
  Platform paths, process invocation and interactive-shell boundaries follow [the platform and shell contract](contracts.md#platforms-and-shells).
- **S4 Honest claims.**
  Nothing is described as supported on a platform or runtime until its acceptance has passed there (R1.27).
  CI on macOS is not live macOS compatibility (R1.30).
  Evidence states what was checked and what was not.
- **S5 Public safety.**
  No credentials, tokens, keys, personal preferences, private learnings, machine or project roster, account names or private paths appear in code, tests, docs, commit messages or pull request text (R1.2, R1.10).
- **S6 Small and specific.**
  Each pull request is one coherent change with no fixed line cap.
  No generic frameworks, plugin registries, event buses or always-on services beyond what a requirement needs (R1.29); concrete types come before abstractions.
- **S7 Dependencies.**
  The standard library comes first.
  Each new third-party module, CI action or tool version is recorded with its reason in a decision under `docs/decisions/`.
- **S8 Custody.**
  Code never deletes unlanded work (R1.18) and never closes or stops Herdr tabs, workspaces or sessions it did not create (R1.54).
- **S9 Release integrity.**
  Workflows hold least-privilege permissions; only a pushed version tag publishes a release; every release artifact is listed in `SHA256SUMS`.
- **S10 Documentation.**
  Markdown has one sentence per line.
  Engineering decisions are recorded under `docs/decisions/` by the pull request that makes them, and user-visible commands are described in the README.
- **S11 Contracts.**
  New or changed behaviour at a module, process, operating-system, shell or terminal boundary states its contract first and follows `docs/contracts.md`.
  A test double that replaces a real boundary passes the same contract test as the real implementation.
  No interface is added for a pure function, a value type or a dependency with one implementation and no test that replaces it.
