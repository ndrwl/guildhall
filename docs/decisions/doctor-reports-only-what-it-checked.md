---
status: accepted
date: 2026-10-20
topic: foundation
formerly: D6, 0006-doctor-reports-only-what-it-checked.md
---

# `doctor` reports only what it checked, and a missing tool is not a failure

## Context

`guildhall doctor` reports the tools Guildhall uses, and a machine may lack some of them without being broken for the work it does.

## Decision

The user-facing `guildhall doctor` report is described in the README.
A missing tool is reported, not treated as a failure, so `doctor` exits 0 whenever it could write its report.
Each version command is bounded by a timeout.

## Consequences

A script cannot use `doctor`'s exit status to detect a missing tool; it must read the report.
A hung tool cannot hang `doctor`.
