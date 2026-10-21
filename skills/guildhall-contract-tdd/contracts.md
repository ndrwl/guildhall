# Contract vocabulary

These are the words the build and review stages share; use them exactly.

## Shape

- Module: anything with an interface and an implementation, from a function to a package.
- Interface: everything a caller must know to use a module correctly, including its errors, ordering and invariants, not only its signatures.
- Contract: the interface written down, in doc comments and the package's contract page, before the implementation.
- Seam: a place where behaviour can change without editing the caller, and where tests observe it.
- Adapter: one concrete thing that satisfies an interface at a seam.
  One adapter means a hypothetical seam and two mean a real one, so a new interface needs a second adapter or a test that must replace the first.
- Depth: much behaviour behind a small interface is deep; an interface nearly as wide as its implementation is shallow.

## What a contract states

- Operations: what each call does, its inputs and outputs, and which values are valid.
- Errors: each error a caller can tell apart, the condition that produces it and the state left behind.
- Ownership: who creates, holds, releases and may delete each resource, file, process or lock.
- Concurrency: which calls may overlap, and what a caller sees when two race.
- Idempotency: what a repeated call does, and whether a retry after a lost reply is safe.
- Identity: what names a thing, and how a stale or reused name is told apart.
- Recovery: what a crash or restart at each point leaves, and how the next run continues.
- Platforms: what differs on Windows, Linux and macOS or between shells, and the error that names a limit rather than emulating it.

## Test terms

- Contract test: a test that exercises a contract only through its interface.
- Test double: a stand-in for a real boundary, used only at system boundaries.
- Shared contract suite: one test function that takes a constructor and runs the same cases against the real adapter and the double.
- Red run: the run where a new case fails on behaviour, before the implementation that passes it.
- Green run: the run where every case and the repository's checks pass.
- Pin: a new test that passed on its first run against existing behaviour; it pins the contract but is not red-then-green evidence.

## Dependencies

- In-process: pure computation; test it directly with no seam.
- Local-substitutable: files, a local database or the test binary as a program; test against the real thing in a temporary directory.
- Remote but owned: your own service over a network; define the port, and test with an in-memory adapter that passes the shared suite.
- True external: a third party you do not control; inject it as a port and replace it with a double that passes the shared suite.
