# Finding the copy from the current directory

This decision supersedes the environment-variable parts of [D65](0065-director-status-feed.md) and [D82](0082-director-handoff-reload.md): no environment variable selects a Director copy any more.
A command finds its copy by `--copy`, or else by **copy discovery** from the current directory.
D65's `needs_me` commands stay written without `--copy`, and D82's turn hooks stay as they were, except that they now name the copy, as below.

## Why

The Guildmaster restarts a Director by stopping it and typing `guildhall director start` in the Director's Herdr tab.
That needed `--copy <dir>` every time, because the only other way to name a copy was an environment variable the session set for its own children, which a Guildmaster's shell never has.
The tab's shell already runs in the copy directory (`director start --tab` opens it there), so the current directory can say which copy is meant.

## The copy marker

A copy is marked by `.guildhall-copy` at its root, one JSON object: `{"schema":"guildhall.copy","version":1,"name":"<copy name>"}`.
A marker makes discovery an explicit claim rather than a guess from file names such as `config.toml`, which repositories also hold.

- A command run with `--copy <dir>` writes the marker, named for the directory's base name, when `<dir>` has none, so `store init --copy <dir>` marks a new copy and every existing copy is marked at its first use with `--copy`.
  A bare `store init <path>` writes none: it cannot tell it is making a copy.
- The marker is created exclusively (`O_CREATE|O_EXCL`), so concurrent first uses cannot race, and an existing marker is never rewritten.
- A copy that cannot be written gets one warning line and the command goes on, so read-only commands such as `director status` and `task list` keep working on a read-only copy.
- A marker that cannot be read, is not JSON, or has an unknown schema or version is an error naming the file; discovery does not skip it to look further up, since a broken marker means a copy was meant there.

## Precedence

1. `--copy`, exactly as before, including the rule that a path flag given with it must name the copy's own file.
2. Explicit `--store`, `--config` or `--state` with no `--copy`: copy selection is bypassed and discovery does not run, so a launched child given explicit paths keeps them wherever it runs.
3. Copy discovery: from the cleaned absolute current directory, walk up one parent at a time to the filesystem root (on Windows, the drive root) and stop at the first directory holding `.guildhall-copy`.

With none of them, every command prints the one line `no Guildhall copy found from <cwd>; pass --copy <dir> or cd into a copy` after its command name, with the exit status it gave for a missing copy before.

## The `state/` rule

If the start directory is the found copy's `state/` or lies under it, the result is no copy, and the search does not go on past that copy.
Every Artisan working directory and attempt directory is `<copy>/state/...`, and an Artisan launched from a Director session must never reach the Director's store by running a bare `guildhall` command.
The rule is a comparison with the found copy's own `state/`, cleaned and absolute and ignoring case on Windows as `samePath` compares, not a skip of any directory named `state`.
Launchers already pass the copy explicitly (`artisan start --copy <dir> --advance`), so nothing legitimate depends on discovery there.

## Removing the variable

The environment variable is gone, both set and read: `SessionSpec.SessionEnv` no longer names the copy, and a value left set in a shell is ignored.
It also leaked: attempt launchers start with the supervisor's environment, so an Artisan launched from a Director session inherited it; removing it closes that, and the `state/` rule keeps discovery from reopening it.

What used it is redone:

- **Director session.** The session directory lies under the copy (`<copy>/director/claude`); that is now a rule of the adapter contract, asserted by the conformance kits instead of the variable, so the session's commands find the copy by discovery.
- **Hooks and status line.** The generated Claude settings give `turn-started`, `turn-ended`, `wait-wake` and `context-report` the copy as `--copy '<copy>'`, single-quoted for a POSIX shell with any `'` escaped.
  This reverses D82's reason for naming no path in a hook: a failed `turn-ended` or `wait-wake` stalls the supervisor or stops wakes, so these must not depend on the directory Claude Code runs a hook in once the Director has `cd`'d into a repository.
- **Lanterns.** The session renders `calm.json` beside the plugin, `{"preference":"<copy>/director/lanterns-calm"}`, and the plugin reads it from its own root; without it Calm is unavailable, as it was with no copy.
- **Skills.** `director status --json` prints the absolute `copy`, from which the self-review skill takes `<copy>/quality/`; the Director tasks skill says bare commands work because the session runs inside its copy, and to use `git -C` rather than `cd` for repositories.

## Accepted limitation

A bare restart typed in the tab records no Herdr pane, because Guildhall ignores Herdr's ambient pane variables.
Wakes then come only through the `wait-wake` hook, which is the primary path, and a later start does not retire that tab.
`director start --tab` still records the binding.
