# D16. Contract-first development and package layout

Guildhall is developed contract first: a change states its contract, tests it and then implements it (`docs/contracts.md`).
Packages stay organised by domain under `internal/`, with colocated tests and `testdata/`, following the Go module layout guidance rather than global interface, implementation or test directories.
Interfaces are declared by their consumers at real seams; pure code stays as functions, and the store keeps no generic storage interface (D9) while a consumer may declare a narrow one over the methods it uses.
A test double that replaces a real boundary shares one contract test with the real implementation.
