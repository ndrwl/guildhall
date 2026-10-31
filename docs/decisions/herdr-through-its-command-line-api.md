---
status: accepted
date: 2026-10-20
topic: herdr
formerly: D17, 0017-herdr-through-its-command-line-api.md
---

# Guildhall drives Herdr through its command-line API

## Context

Guildhall needs to create and drive Herdr tabs and panes on every supported platform.
No Go client library for Herdr was found.

## Decision

Guildhall drives Herdr by running the `herdr` client with argument lists and reading the JSON it prints, as Herdr's own agent guide documents.
Responses recorded from Herdr 0.9.5 are kept under `internal/herdr/testdata`, so tests run without Herdr installed.

## Alternatives rejected

- Implementing Herdr's socket protocol in Guildhall: the client already handles each platform's transport (a Unix socket, or a named pipe on Windows).

## Consequences

Each Herdr operation costs a process start, and Guildhall depends on the client's argument and JSON formats, which the recorded responses pin.
