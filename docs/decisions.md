# Engineering decisions

Numbered engineering decisions, recorded by the pull request that makes them.
A later decision may supersede an earlier one by number; entries are not rewritten.
Product requirements live in `docs/requirements.md`.

## Convention

Each decision is its own file under `docs/decisions/`, so parallel pull requests never edit the same lines.
A numbered decision `Dn` lives in `docs/decisions/NNNN-<slug>.md`, where `NNNN` is `n` padded to four digits.
A pull request adds a new decision as `docs/decisions/<branch-or-topic-slug>.md`, with no number and a heading without one, and does not edit this index.
After it merges, a follow-up refresh renames each unnumbered file to the next free number, adds the number to its heading and adds its line below.
Until then a new decision is cited by its file path rather than a number.

## Decisions

- [D1. One Go module and one binary](decisions/0001-one-go-module-and-one-binary.md)
- [D2. Go 1.27](decisions/0002-go-1-27.md)
- [D3. Standard library only for the foundation](decisions/0003-standard-library-only-for-the-foundation.md)
- [D4. CI matrix and lint](decisions/0004-ci-matrix-and-lint.md)
- [D5. Release builds](decisions/0005-release-builds.md)
- [D6. `doctor` reports only what it checked](decisions/0006-doctor-reports-only-what-it-checked.md)
- [D7. Release policy regression test](decisions/0007-release-policy-regression-test.md)
- [D8. TOML parsing for copy configuration](decisions/0008-toml-parsing-for-copy-configuration.md)
- [D9. bbolt for the store](decisions/0009-bbolt-for-the-store.md)
- [D10. Store single-writer lock](decisions/0010-store-single-writer-lock.md)
- [D11. Repository paths are absolute in the owner machine's form](decisions/0011-repository-paths-are-absolute-in-the.md)
- [D12. Steward rollouts copy files with scp and nothing else](decisions/0012-steward-rollouts-copy-files-with-scp.md)
- [D13. Rollout plans, previews and receipts live in a plan directory](decisions/0013-rollout-plans-previews-and-receipts-live.md)
- [D14. Separate managed publication from Director-owned config](decisions/0014-separate-managed-publication-from-director-owned.md)
- [D15. Local messaging in the store](decisions/0015-local-messaging-in-the-store.md)
- [D16. Contract-first development and package layout](decisions/0016-contract-first-development-and-package-layout.md)
- [D17. Herdr through its command-line API](decisions/0017-herdr-through-its-command-line-api.md)
- [D18. Explicit Herdr session and version floor](decisions/0018-explicit-herdr-session-and-version-floor.md)
- [D19. Version 1 result file](decisions/0019-version-1-result-file.md)
- [D20. Typing the launch command into a new tab](decisions/0020-typing-the-launch-command-into-a.md)
- [D21. Fake adapter as a subcommand](decisions/0021-fake-adapter-as-a-subcommand.md)
- [D22. Workflow approvals per Director, keyed by digest](decisions/0022-workflow-approvals-per-director-keyed-by.md)
- [D23. Typed decisions bound to an artefact](decisions/0023-typed-decisions-bound-to-an-artefact.md)
- [D24. Inspection reads from store receipts](decisions/0024-inspection-reads-from-store-receipts.md)
