# Hallmark <date>

Previous: <commit of the previous hallmark, or "none">.
Current: <commit reviewed>.
Merged since: <pull request links>.

## Verdict

<three to five sentences: what improved, what costs most over time, and what must land before the next slice>

## Validation

| Command | Exit | Result |
|---|---|---|

<what could not run and the evidence it leaves missing>

## Earlier findings

| # | Finding | Disposition | Evidence |
|---|---|---|---|

## Findings

| # | Finding | Evidence | Impact | Fix | Size | Urgency | Strength |
|---|---|---|---|---|---|---|---|

## Fixes

### H1. <title>

Contract: <what the doc comments and the contract page will say, with errors, ownership, concurrency, identity and recovery where they apply>.
Consumers: <modules that call it>.
Tests: <new red-first tests>; <pins that pass today>; <shared real and fake suites, native and compatibility checks>.
Steps: <one pull request, or add, compose and delete with the deletion gate>.
Size: <lines>.

## Streams

<the parallel streams, the nouns they share and any drift between them>

## Decisions

| Decision | Keep or reopen | Better option | Cost now | Cost later |
|---|---|---|---|---|

## Considered and not raised

- <candidate>: <why the deletion test failed>.

## Questions

**Q1. <direction change>**
Recommended: <answer>, because <reason>.
Alternative: <what, and its cost>.

## Procedure

- <what this checklist should change next time>.
