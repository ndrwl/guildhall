---
status: accepted
date: 2026-10-20
topic: foundation
formerly: D16, 0016-contract-first-development-and-package-layout.md
---

# Guildhall is developed contract first, with packages organised by domain

## Context

Every change crosses real seams (store, processes, git, forges), and a test double that drifts from the real implementation would pass tests while the product fails.

## Decision

A change states its contract, tests it and then implements it (`docs/contracts.md`).
Packages stay organised by domain under `internal/`, with colocated tests and `testdata/`, following the Go module layout guidance.
Interfaces are declared by their consumers at real seams; pure code stays as functions.
The store keeps no generic storage interface ([bbolt for the store](bbolt-for-the-store.md)), while a consumer may declare a narrow one over the methods it uses.
A test double that replaces a real boundary shares one contract test with the real implementation.

## Alternatives rejected

- Global interface, implementation or test directories: they separate a contract from the code and tests that hold it.

## Consequences

Each package has a contract page under `docs/contracts/`, and a double cannot be added without its shared contract test.
