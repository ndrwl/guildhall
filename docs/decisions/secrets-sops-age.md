---
status: accepted
date: 2026-11-04
topic: steward
tasks: d130
amended-by: steward-secrets-push.md
---

# A machine's secrets are sops+age ciphertext encrypted to its own key, decrypted in memory at start

## Context

d130 asked how each Director machine gets the secrets it needs, such as API keys, without pasting them into config, task records, messages or logs (R1.10).
Sign-ins for subscriptions, Claude Code and Codex, and `gh`'s forge sign-in stay in each runtime's own store on each machine, checked by [`guildhall signin`](../signin.md); this record is about every other secret.

## Decision

Each machine has its own age key and one secrets file, `~/.config/guildhall/age.key` and `secrets.sops.yaml`, both outside every copy, since copies are backed up file by file and never pruned.
`guildhall secrets init` creates the key owner-only and prints only its public key.
`director start` and `steward start` run the `sops` program once to decrypt the file into memory, and a file that fails to decrypt refuses the start naming no value; no key or no file means no injected secrets.
Behaviour is on the [secrets page](../secrets.md) and the [contract](../contracts/secrets.md).
R1.10 is reworded to say so.

## Alternatives rejected

- Infisical, self-hosted or cloud: a service to run or pay for, and a Director could not start without reaching it.
- The 1Password CLI: a subscription and a signed-in desktop app or service account on every machine.
- A Go age or sops library: a module dependency, against [standard library only](standard-library-only-for-the-foundation.md); running the programs adds none.
- One shared key for every machine: losing one machine would expose every machine's file.

## Consequences

`sops` and `age-keygen` are tools a machine with secrets needs on `PATH`; a machine with none needs neither.
Revoking a lost machine cannot recall its ciphertext, key or the files' git history, so it means rotating every value it could read, which the later Steward change makes part of retiring a machine.
Windows is refused by `secrets init` until an owner-only key is built and tested there (R1.27).
