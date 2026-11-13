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
On an enrolled machine [the Steward pushes the file](#the-steward-pushes-them).
On a standalone one the Guildmaster writes it there with `sops`, such as `sops --age <public key> --encrypt --input-type dotenv --output-type yaml plain.env > ~/.config/guildhall/secrets.sops.yaml`, from a plaintext file they then delete.

## secrets init

```sh
guildhall secrets init
```

`secrets init` creates the machine key by running `age-keygen` and writing its output itself, owner-only, and prints its path and its public key, the `age1…` recipient a secrets file is encrypted to, which is not a secret.
It never prints the secret key.
With a key already there, it keeps it and prints its public key, read with `age-keygen -y`.
It needs `age-keygen` on `PATH`, and is refused on Windows, where an owner-only key is not yet supported (R1.27).

## secrets check

```sh
guildhall secrets check [--json] [--copy <dir>]
```

`secrets check` reports this machine's key, with its public key, and its secrets file, with the file's sha256, whether it decrypts with the key and the names it holds, never a value, and changes nothing.
With `--copy`, a key or file inside that copy is refused.
Without `--json` it prints one line each and exits 1 when the file exists but does not decrypt; with `--json` it prints the report as one JSON document and exits 0, so the Steward, which runs `secrets check --json --copy <copy_dir>` over SSH, reads why.
The report's `installs` is `true` from a `guildhall` that has [`secrets install`](#secrets-install); an older one leaves it out, and the Steward refuses to push to that machine.

## secrets install

```sh
guildhall secrets install --expect <sha256> --copy <dir>
```

`secrets install` is how a [Steward push](#secrets-plan-and-apply) replaces the secrets file; it is not for daily hand use.
The Steward first puts the new ciphertext beside the file as `secrets.sops.yaml.new-<sha256>`, then runs it over SSH. It:

1. refuses a key or file inside the copy `--copy` names, as `secrets check` does;
2. refuses when `secrets.sops.yaml.new-<expect>` is missing or its sha256 is not `--expect`;
3. refuses when the machine has no key or the new file does not decrypt with it, by the same check `secrets check` runs;
4. copies the current secrets file, if there is one, to `secrets.sops.yaml.prev`, replacing any earlier one; a first push keeps none;
5. renames the new file over `secrets.sops.yaml` in one rename, so the file's name always holds the old or the new ciphertext;
6. removes every other `secrets.sops.yaml.new-*` file, such as one a cut-short push left.

On a refusal the secrets file is untouched and the new file is removed.
It always prints one JSON document, naming the files, the digests, the names the new file holds and, on a refusal, the `step` that refused (`paths`, `digest`, `decrypt`, `previous` or `rename`) and the `problem`, never a value, and exits 0 when it installed the file and 1 when it refused.

To go back to the file a push replaced, run on the machine `mv ~/.config/guildhall/secrets.sops.yaml.prev ~/.config/guildhall/secrets.sops.yaml`; the next push from the private config replaces it again.
There is no remote restore command.

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

## The Steward pushes them

The [Steward](steward.md) keeps each enrolled machine's secrets as ciphertext in the private config and pushes it, by a command separate from rollouts, so a rollout never carries a credential ([decision](decisions/steward-secrets-push.md)).

**Keys.** Each machine's [roster](steward.md#rostertoml) entry records `age_recipient`, the public key `guildhall secrets init` printed on it, and the roster records `recovery_recipient`, the public key of the Guildmaster's recovery key.
Every machine's file is encrypted to its own key and the recovery key, which lets the Steward add a value to a file it cannot otherwise read.
The recovery key lives on the Steward machine at `~/.config/guildhall/recovery.key`, outside every copy, owner-only; create it there with `age-keygen -o ~/.config/guildhall/recovery.key` and put the public key it prints in the roster.

**Files.** The private config holds `secrets/<machine>.sops.yaml` for each machine, and `.sops.yaml`, generated from the roster with one rule per machine, so `sops` run by hand on a file encrypts it the same way.
A value several machines need is set on each of them, so removing one machine never re-encrypts another's file.

### secrets set

```sh
guildhall steward secrets set --config <steward.toml> <machine> <NAME>
```

`secrets set` reads the value from the terminal, asking on standard error with echo off, or else from standard input, all of it but one final line ending; a value on the command line, where the process list and shell history would show it, is refused.
It holds the private config's `.rollout.lock`, decrypts the machine's committed file, if there is one, in memory with the recovery key, adds or replaces the name, and has `sops` encrypt the result from its standard input to the machine's key and the recovery key.
It then commits only that file and `.sops.yaml`, through a private index as [collection](steward.md#collection) does, pushes the private config, and prints the file's names, its sha256 and the commit, never the value.
A machine with no `age_recipient`, a roster with no `recovery_recipient`, or an existing file with no recovery key to decrypt it is refused before anything is written.

### secrets plan and apply

```sh
guildhall steward secrets plan --config <steward.toml> --machine <label> [--machine <label>...] --plan <new dir>
guildhall steward approval request --plan <dir> [--copy <steward copy>]
guildhall steward secrets apply --config <steward.toml> --machine <label> [--machine <label>...] --plan <dir> --approve <plan digest>
```

The plan reads each machine's file at the private config's `HEAD` and runs the fixed, read-only remote command `secrets check --json --copy <copy_dir>` on the machine, with its first Director copy, and changes nothing.
It writes `plan.json`, a `guildhall.secrets-plan/1` document, and each changing machine's ciphertext under `files/<machine>/`, and prints each machine's status, `change`, `unchanged` or `refused`, its names, its file's digest now and after, and the commands, with the plan digest and the apply command.
The plan holds names and digests, never a value.
A machine is refused when it is a Windows machine (R1.27), has no Director copy, no `age_recipient`, no committed file or one not encrypted to exactly its key and the recovery key, or cannot be checked, or when its key's public key is not its `age_recipient`, or its secrets file is not at `~/.config/guildhall/secrets.sops.yaml`.
A machine that would change is also refused when its `guildhall` predates [`secrets install`](#secrets-install): upgrade it there first.

Every secrets plan needs the Guildmaster's approval of its exact digest: `steward approval request` opens the decision `secrets-<first 12 hex digits>` on the reserved task `steward/secrets`, which only the Guildmaster answers, outside any session, and inside one `--approve` stands only on their answer `approve` ([approval in a Steward session](steward.md#approval-in-a-steward-session)).

Apply holds `.rollout.lock`, plans again and refuses, writing nothing, unless the digest is the same and the staged ciphertext intact, then, on each machine that changes:

1. copies the ciphertext beside the secrets file, to `~/.config/guildhall/secrets.sops.yaml.new-<sha256>`, with `scp`;
2. reads that file back and compares its sha256 with the plan;
3. runs [`secrets install --expect <sha256> --copy <copy_dir>`](#secrets-install) there, which renames it over the secrets file only once it decrypts with the machine's own key, keeping the file it replaces as `secrets.sops.yaml.prev`;
4. runs `secrets check --json` there, which must find the installed file with the pushed digest, decrypting to the planned names.

Until step 3 renames it, the machine's secrets file is untouched, so a cut-short copy, a read-back mismatch or a file that does not decrypt there leaves it as it was.

The receipt `receipt-<UTC time>.json` in the plan directory records each machine's status, `applied`, `unchanged`, `failed`, `verify_failed`, `install_failed` or `check_failed`, its names, its digests and every command with what `secrets install` and `secrets check` printed, never a value.
`install_failed` means `secrets install` refused, its report, in the step's error, naming the step that refused; the secrets file is unchanged.
A plan written before `secrets install` existed is stale, since its commands are in its digest: apply refuses it, and a new plan is needed.
A failure on one machine does not stop the others, and apply exits 1 unless every machine is applied or unchanged.
The machine's sessions decrypt the new file at their next start.

## Retiring a machine

```sh
guildhall steward machine retire --config <steward.toml> --plan <new dir> <machine>
guildhall steward approval request --plan <dir> [--copy <steward copy>]
guildhall steward machine retire --config <steward.toml> --plan <dir> --approve <plan digest> <machine>
guildhall steward machine retire --config <steward.toml> --status <machine>
guildhall steward machine retire --config <steward.toml> --rotated <NAME> <machine>
```

A lost or retired machine keeps its age key and the ciphertext it was given, and the private config's history keeps every file it was ever given, so removing its recipient stops only future pushes: **revoking a machine means rotating every value it could read** ([decision](decisions/steward-machine-retire.md)).

The plan reads the machine's `age_recipient`, its `secrets/<machine>.sops.yaml` and every other machine's file at the private config's `HEAD`, changes nothing, and writes `plan.json`, a `guildhall.retire-plan/1` document, holding names and digests only. It prints:

- the `age_recipient` it removes from `roster.toml` and `.sops.yaml`, and the file it removes, with its sha256 and names;
- every name in that file, as a manual step: rotate it at its provider, then set the new value with [`secrets set`](#secrets-set) on each other machine whose file holds that name, or, when none does, have the Guildmaster mark it rotated with `--rotated`;
- the machine's own credentials to revoke at their providers: its GitHub sign-in and tokens, its SSH keys and the Steward's `Host` entry for it, its Herdr profile, and its Claude Code and Codex sessions, none of which was ever copied to another machine ([sign-in](signin.md)).

It is refused for a machine not in the roster, one already retired, whether its retirement is open or closed, a `roster.toml` that differs from `HEAD`, or one that does not write the machine's `age_recipient` on a line of its own.
The Guildmaster approves the plan by its exact digest, the decision `retire-<first 12 hex digits>` on the reserved task `steward/secrets`, as for a secrets push.

Apply holds `.rollout.lock`, plans again and refuses unless the digest is the same, then in one commit, pushed:

1. removes the machine's `age_recipient` line from `roster.toml`, and nothing else, and regenerates `.sops.yaml` without it;
2. removes `secrets/<machine>.sops.yaml`; no other machine's file is touched, as each has its own;
3. writes `retired/<machine>.json`, a `guildhall.retire-receipt/1` record, also copied to the plan directory as `receipt.json`: the names, the commit the file was read at, each rotation the retirement waits for, and the revocation steps. There is a rotation for every name: one for each other machine that holds it, by name and machine, or, when none does, one by name alone.

The machine stays in the roster, so a rollout to it fails until it is removed by hand; nothing is run on the machine, which may be gone.
The retirement stays `open` until every rotation closes, so every name the machine could read is waited for:

- A name another machine holds closes when `secrets set` of that name on that machine gives it a new value, in the same commit, recording its date. A value the retired machine could read does not count: neither the one the machine already held nor the one in the retired machine's own file, which `secrets set` decrypts with the recovery key at the commit the plan read it at. A rotation on a machine that is itself retired later closes with that retirement.
- A name no other machine holds has nothing to set, so it closes when the Guildmaster, having rotated or revoked it at its provider, runs `--rotated <NAME>`. That holds `.rollout.lock`, records the date and closes the rotation in one pushed commit, and is refused for a name another machine holds. It records the Guildmaster's word, so it is refused inside a Guildhall session: an agent cannot mark a value rotated.

`--status` prints the record, with each rotation's state and date; it is `closed` once none is waiting, and at once only when the machine's file held no names.
Guildhall cannot check the revocation steps or that a marked value was really rotated; the Guildmaster follows them at each provider.
