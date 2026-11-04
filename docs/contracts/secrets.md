# `internal/secrets`

## Contract

The package is a machine's [injected secrets](../secrets.md): its age key and sops secrets file, decrypted in memory, withheld from model-driven processes and redacted from what their wrappers write.
It runs the `sops` and `age-keygen` programs and imports only the standard library ([decision](../decisions/secrets-sops-age.md)).

- `DefaultPaths()` is `age.key` and `secrets.sops.yaml` in `GUILDHALL_SECRETS_DIR` (`DirEnv`) when set, and otherwise in `~/.config/guildhall` (`Dir`); `Paths.Outside(dir)` refuses either file inside `dir`, comparing absolute paths with symbolic links resolved.
- `Load(ctx, paths, run)` is no values and no error when the key or the file is missing.
  Otherwise it runs `sops --decrypt --output-type json <file>` once with `SOPS_AGE_KEY_FILE` set, and reads a flat JSON object of names to strings, skipping `sops`'s metadata key.
  A failed run, plaintext that is not such an object, a name that is not an environment variable name or a value that is not a string or holds a NUL byte is an error wrapping `ErrDecrypt`, holding `sops`'s standard error and never its standard output or a value.
- `Values` prints its names only, under `%v`, `%+v` and `%#v`; `Env`, `Names` and `Pick` give its entries sorted by name.
- `Strip(environ)` drops every variable `GUILDHALL_SECRET_NAMES` (`NamesEnv`) lists, keeping that variable, so a process started from the result strips the same names.
- `NewRedactor(values)` replaces each value of at least `MinRedacted` (4) bytes with `[redacted]`, longest first; `FromEnv(getenv)` is the redactor of the values of the names `GUILDHALL_REDACT_NAMES` (`RedactEnv`) lists; a nil redactor redacts nothing, and its `Writer` redacts each write on its own.
- `Init(ctx, paths, run)` refuses Windows (`ErrWindows`).
  With no key it creates the key's directory `0700`, runs `age-keygen`, requires its output to hold a secret key and a `# public key: age1…` line, and writes it to a new file `0600`, returning the public key and `true`.
  With a key it keeps it and returns the public key `age-keygen -y` prints, and `false`.

## Tests

- `secrets_test.go`: `Strip`, name lists, redaction of whole and nested values, `FromEnv`, `DefaultPaths` with and without `GUILDHALL_SECRETS_DIR`, and `Outside`.
- `fake_unix_test.go`, over fake `sops` and `age-keygen` first on `PATH`: no key or no file is no values; a good file decrypts, and printing its `Values` shows no value; a failing `sops` and plaintext that is not a flat map are `ErrDecrypt` holding no value of the fake's standard output; `Init` creates an owner-only key in an owner-only directory and a second `Init` keeps it.
- `internal/cli` `secrets_test.go`: `secrets init`, the start preflight's line and refusals, the runtime's environment without injected names, and an Artisan inheriting only its profile's `inherit_env`; `internal/launch` `secrets_test.go`: the worker's environment; `internal/adapter/claude` `TestRunRedactsInheritedSecrets`: the wrapper's output, event file and result.
