---
status: accepted
date: 2026-11-04
topic: steward
tasks: d130
amends: steward-fixed-remote-commands.md, secrets-sops-age.md
amended-by: steward-machine-retire.md
---

# The Steward pushes each machine's secrets ciphertext by an approved plan, and adds a read-only secrets check

## Context

A machine decrypts its own sops secrets file in memory at start ([decision](secrets-sops-age.md)).
d130 Slice 3 has the Steward give each Director machine that file without a value ever being written in clear, put on a command line, or recorded in a plan, receipt, commit, message or log, and without rollouts carrying credentials (R1.8, R1.10).
The Steward needs to confirm a pushed file decrypts on the machine, which the existing [fixed remote commands](steward-fixed-remote-commands.md) cannot do.

## Decision

- The roster records each machine's age public key as `age_recipient` and the Guildmaster's recovery key's as `recovery_recipient`; neither is a secret.
- Each machine has its own ciphertext, `secrets/<machine>.sops.yaml` in the private config, encrypted to its key and the recovery key, and `.sops.yaml` is generated from the roster with one rule per machine. A value several machines share is copied into each machine's file, so removing one machine re-encrypts no other's.
- `steward secrets set <machine> <NAME>` reads the value from the terminal without echo, or from standard input, and refuses one on the command line. It decrypts the machine's committed file in memory with the recovery key, kept on the Steward machine outside every copy, adds the value, and has `sops` encrypt the result from its standard input. It commits only that file and `.sops.yaml` through a private index under `.rollout.lock`, and pushes.
- `steward secrets plan|apply` is a separate command from `steward rollout`, so rollout plans, digests and receipts stay credential-free. It pushes the committed ciphertext with the same scp, read-back digest, plan digest and receipt as a rollout, and every secrets plan needs the Guildmaster's approval of its digest, on the reserved task `steward/secrets`. Plans and receipts hold names and digests only.
- The allow-list gains one entry of the existing shape: `secrets check --json --copy <copy_dir>`. It only reads: it prints the machine's key path and public key, its secrets file's path and sha256, whether it decrypts with the key, and its names, never a value. The copy directory is one of the machine's Director copies, which the key and file must be outside. The plan uses it to read the machine's current digest and confirm its key is the roster's, and apply uses it after the read-back to confirm the machine decrypts the file to the planned names.

## Alternatives rejected

- Carrying secrets in a rollout: it would put credentials into managed settings' plans and receipts, which `docs/steward.md` keeps credential-free.
- `sops set` with the value as an argument: it would show in the process list.
- One shared secrets file encrypted to every machine: removing a machine would re-encrypt every other machine's file, and every machine could read every value.
- A remote `secrets check` with no copy directory: it would widen the one-variable shape of every remote command for one command.

## Consequences

A machine whose `guildhall` predates `secrets check` is refused by the plan until it is upgraded.
The machine's `~/.config/guildhall` must exist before a push, as `scp` creates no directory; `guildhall secrets init` creates it with the key the push needs anyway.
Secrets are pushed to Linux and macOS machines only until acceptance passes on Windows (R1.27).
A lost machine keeps its old ciphertext and key, so revoking it means rotating every value it could read, which [`steward machine retire`](steward-machine-retire.md) plans and tracks.
