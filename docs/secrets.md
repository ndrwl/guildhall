# Secrets

A Director machine gets the secrets it needs, such as an API key a repository's tests call, without any of them being written in clear to config, task records, messages, prompts or logs.
Sign-ins are not secrets here: each runtime keeps its own on the machine it runs on, checked by [`guildhall signin`](signin.md), and Guildhall never stores or copies one (R1.10).
Every other secret is kept as [sops](https://github.com/getsops/sops) ciphertext encrypted to the machine's own [age](https://age-encryption.org) key, and exists in clear only in a process's memory ([decision](decisions/secrets-sops-age.md)).

## Where they live

Each machine has two files, both outside every [copy](running.md#copies):

| File | What it is |
|---|---|
| `~/.config/guildhall/age.key` | The machine's age key, readable by its owner only (`0600`, in a `0700` directory). |
| `~/.config/guildhall/secrets.sops.yaml` | The machine's secrets file: names and values, the values encrypted to the machine's public key with sops. |

A copy is backed up file by file into a backup that is never pruned, so neither file may ever sit inside one: a start, or an Artisan's launch, refuses a key or secrets file inside its copy.
`GUILDHALL_SECRETS_DIR`, when set, names the directory used instead of `~/.config/guildhall`; the test guards set it to an empty directory ([testenv](contracts/testenv.md)), and a machine has no reason to.

Each name is an environment variable name, such as `OPENAI_API_KEY`, and each value a string.
Until the [Steward pushes secrets files](#not-yet), the Guildmaster writes the file on the machine with `sops`, such as `sops --age <public key> --encrypt --input-type dotenv --output-type yaml plain.env > ~/.config/guildhall/secrets.sops.yaml`, from a plaintext file they then delete.

## secrets init

```sh
guildhall secrets init
```

`secrets init` creates the machine key by running `age-keygen` and writing its output itself, owner-only, and prints its path and its public key, the `age1…` recipient a secrets file is encrypted to, which is not a secret.
It never prints the secret key.
With a key already there, it keeps it and prints its public key, read with `age-keygen -y`.
It needs `age-keygen` on `PATH`, and is refused on Windows, where an owner-only key is not yet supported (R1.27).

## At start

[`director start`](director.md#start) and [`steward start`](steward.md#start) decrypt the secrets file once, by running `sops --decrypt` with `SOPS_AGE_KEY_FILE` naming the key, into memory, after the rest of the preflight passes, and print one line:

- `secrets: none injected (no machine key or no secrets file at <file>)` when either file is missing: the machine has no injected secrets, and the session starts exactly as without this feature;
- `secrets: <n> injected (<names>), withheld from model-driven processes but a profile's inherit_env`, naming the names and never a value;
- `secrets: refused: <why>` when the key or the file is inside the copy, `sops` is not on `PATH`, or the file fails to decrypt or is not a flat map of names to strings; the start exits 1, and the reason holds `sops`'s error output, never its standard output, so it names no value.

`--check` runs the same decryption and prints the same line, recording nothing.
A start with `--tab` decrypts nothing itself; the supervisor in the tab does.

The supervisor keeps the values in its own environment, so the `gh` and `git` it runs itself can use them, and lists their names, never their values, in `GUILDHALL_SECRET_NAMES`.

## Model-driven processes get none

A model-driven process gets no injected secret by default ([decision](decisions/no-injected-secret-for-models.md)).
These are the Director's or Steward's runtime, every Artisan's launcher and worker, the pane bridge and their descendants.
Each of their environments is built without the names `GUILDHALL_SECRET_NAMES` lists: the runtime's in the supervisor, the launcher's and the pane bridge's when an attempt starts, and the worker's in the launcher.
Every `herdr` command Guildhall runs gets none either, since it may start the Herdr server whose panes run them.

The one way in is a profile's [`inherit_env`](configuration.md#profiles): an owned Artisan of that profile gets each name it lists that the secrets file holds.
Its launcher decrypts the file itself, once, only when the list is not empty, and adds those names alone to the worker's environment, with `GUILDHALL_REDACT_NAMES` naming them.
A listed name the file does not hold is passed nothing, and a file that fails to decrypt refuses the attempt's start.
This keeps a value away from a model by default; it is not a sandbox: a model-driven process runs as the machine's user, who can read the key.

## Redaction

The Claude and Codex wrappers replace each value `GUILDHALL_REDACT_NAMES` names with `[redacted]` in their output, which the launcher appends to `launcher.log`, in the [Workbench](artisan.md#workbench) event file and in the result they publish, and the Codex wrapper also in the answer document and its final message file.
A value shorter than four bytes is not redacted, since it would match ordinary text, and each write is redacted on its own, so a value the runtime splits across two writes is not found.
Redaction is defence in depth only: the guarantee is that a model-driven process holds no value it was not given.

## Not yet

The Steward pushing each machine's secrets file, with the machine's public key in the roster, and revoking a lost machine by rotating every value it could read, are later changes.
Until then a secrets file is written by hand on its machine, and rotating a value means re-encrypting the file there.
