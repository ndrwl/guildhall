---
name: guildhall-contract-tdd
description: Use in a build or fix stage to implement an accepted spec contract first; state each contract it names, write that contract's tests red, implement to green one cycle at a time, and record both runs.
---

Adapted from `tdd` in mattpocock/skills (MIT, commit 5beb35b); see CREDITS.md.

# Contract TDD

A build stage turns an accepted spec into a code head, and a fix stage changes that head to answer review findings.
Nobody answers you during the stage: the accepted spec, any findings and the repository are your whole brief.
Read the repository's contributing guide and contract documentation first; this skill follows them, and where they say more, they win.
Use the words in `contracts.md` exactly, and see `go-tests.md` for good and bad Go tests.

## The seams are the spec's contracts

A seam is the public boundary a test observes behaviour through, without reaching inside.
Test only at the contracts the accepted spec names.
A contract the spec did not name is an open question in your result, not a new interface.
Add no interface for a pure function, a value type or a dependency with one implementation and no test that replaces it.

## Steps

1. State each contract before its implementation: the operations, inputs and outputs, errors and where they apply, ownership, concurrency, idempotency, identity and recovery, or why one does not apply.
   Write it in the doc comments of the exported types and functions, and in the package's contract page where the repository keeps one.
2. Write the contract's tests one case at a time, and run each new case red before you implement it.
   A case that does not compile is not red: give it the stated types with empty bodies so it fails on behaviour, and keep the failing output.
3. Implement only enough to turn that case green, then take the next case.
4. Where a test double replaces a real boundary, run one contract test function against both the real implementation and the double.
   Guildhall's own examples are `testToolBoundary` in `internal/cli/tools_test.go` and `messagingtest.RunMailboxContract` in `internal/messaging/messagingtest/contract.go`.
5. Run the repository's full checks and keep the passing output.

In a fix stage, each finding that changes behaviour starts at step 2 with a case that reproduces it.

## Rules

- Red before green: no implementation without a failing test that needs it, and nothing beyond what that test needs.
- One cycle at a time: one contract case, one minimal implementation, then the next, each cycle responding to what the last one taught you.
- Test behaviour through the public interface, never internals, so the test survives a rewrite that keeps the contract.
- Mock at system boundaries only: processes, the operating system, terminals, shells, remote hosts, time and randomness.
  Prefer real files, real processes and the test binary acting as the external program to a double.
- Change a test only when it contradicts the accepted spec or a recorded decision, and cite that evidence; never weaken a test to make an implementation pass.
- Refactoring belongs to review, not to this loop.

## Anti-patterns

- Implementation-coupled: the test mocks your own collaborators, reaches unexported state or checks through a side channel, so it breaks on a refactor that keeps the behaviour.
- Tautological: the expected value is computed the way the code computes it, so the test passes by construction; take expected values from the spec, a worked example or a known literal.
- Horizontal cycles: all tests first and then all code, which tests imagined shapes; work in vertical cycles instead, one case through to its code.

## Evidence

The review standard requires the pull request description to show the red and the green runs, so your result carries them.
Record, for each contract, its test functions, the red run of each new case and the final green run, each with its command and exit code.
A test that passes on its first run against existing behaviour is a pin: list it as one, never as red-then-green.
End with any open questions, each with your recommended answer and the choice you proceeded on.

## Scripts

`scripts/check.sh` and its PowerShell twin `scripts/check.ps1` run `gofmt -l .`, `go vet ./...` and `go test ./...` from the repository root with module and toolchain downloads turned off, and stop at the first failure.
They are optional, run nothing else and reach no network or model.
A repository in another language replaces them with a Steward-managed override that keeps the attribution line.
