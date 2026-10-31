---
status: accepted
date: 2026-10-20
topic: dependencies
formerly: D9, 0009-bbolt-for-the-store.md
---

# The store uses bbolt, with JSON records and no generic storage interface

## Context

The store (R1.17) needs durable transactions and a cross-process file lock on Linux, Windows and macOS, while release builds keep `CGO_ENABLED=0` ([release builds](release-builds.md)).

## Decision

The store uses `go.etcd.io/bbolt` v1.5.0, which brings `golang.org/x/sys`.
It is pure Go; it gives serializable transactions with a durable commit, and its kernel file lock works on all three platforms.
Records are JSON from the standard library inside bbolt buckets, so the store needs no other codec.
The store has no generic storage interface: `internal/store` exposes concrete record types and mutation methods only.

## Consequences

The single-writer rule can rest on bbolt's own lock ([store single-writer lock](store-single-writer-lock.md)).
A consumer that wants an interface declares a narrow one over the methods it uses ([contract-first development](contract-first-development-and-package-layout.md)).
