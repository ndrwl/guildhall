# Lanterns contracts

Lanterns is its own visuals-only plugin, separate from the future Clerk interaction plugin.
It ships as embedded files in `internal/lanterns/plugin` and is installed in every generated Claude Director workspace.
The Claude adapter passes its absolute plugin directory to Claude, without symlinks or a separate checkout.
A classic session-start hook supplies the opening-marker convention even without function hooks.
The function-hook adapter wraps the original assistant drawing in a rounded urgency frame; only an opening marker selects red, yellow or green, and unmarked text is neutral.
Without `CLAUDE_CODE_ENABLE_FUNCTION_HOOKS=1`, function hooks perform no reads, registrations, drawing changes or timer work.

The native status line uses `director context-report --lanterns`.
The existing command without that flag retains its exact output and context-file behavior.
The flagged display never writes context; the core `turn-ended` transcript hook owns the new workspace's handoff report independently of Lanterns.
The optional band starts with the overall light, then shows Guildhall status counts and the context figure.
Stalled/retry/resume/recovery items are red; open decisions are yellow; healthy status is green; failed reads are neutral, never silently green.
Status inspection never acknowledges messages, answers decisions or changes workflow records.

Labels merge into the existing schemaVersion 1 file selected by `VITALS_AXI_LABELS`, defaulting to `$XDG_STATE_HOME/bridge-lights/vitals-identities.json` or `~/.local/state/bridge-lights/vitals-identities.json`.
A Director supervisor refreshes them every 15 seconds, independently of function hooks and model turns.
On Linux, only identities matching the current boot and process creation token are published, with descendants enabled, a PID namespace and 60-second expiry.
The Director supervisor anchors the Director label; contained workers anchor Artisan labels, never guessed PIDs.
Released owners, completed attempts, stopped sessions and dead or reused process identities are omitted.
Publication takes a nonblocking advisory exclusive flock on `<labels-file>.lock` around read/merge/atomic replacement; cooperating publishers must use the same lock.
Copy-scoped IDs replace only this copy's Lanterns entries, retaining other publishers' entries and unknown fields in a live generation with the same boot and PID namespace only when each process selector still matches a live PID and start ticks.
Every refresh drops foreign entries with exited, reused or unverifiable processes and unsupported selector kinds before checking the merged-entry limit; it adds no per-publisher expiry metadata.
An expired or incompatible generation is replaced; malformed files, unsupported schemas, lock contention and more than 1000 merged entries fail without changing the file.
The shared expiry is at least 60 seconds from refresh and never shortens a preserved publisher's expiry.
Non-Linux systems do not publish Linux selectors.
Failures are advisory and cannot stop or mutate the session; stale files expire when no publisher refreshes them.
While any publisher continues refreshing, labels anchored to exited processes disappear on its next successful refresh; live matching processes remain eligible for preservation.

Calm is off by default and persists at `<copy>/director/lanterns-calm`, outside the replaced plugin workspace, across Director sessions.
Its toggle persists before changing the live choice; a write failure leaves the choice unchanged.
Without `GUILDHALL_COPY`, Calm registers no command, reads or writes no preference and leaves native tool and Spinner drawings unchanged.
Calm hides only routine tool drawings, leaving messages, execution and stored transcripts untouched.
Its terminal Spinner is a bounded Raster with a blue smithy roof `▁▟███▙▁`, chimney and orange glow `▄` pulsing every 880 ms.
Smoke `○ ◌ ·` rises, drifts right and fades on the 220 ms tick.
Unmounted sites stop blitting, narrow viewports clip without wrapping, and Calm off restores native drawings.

Tests are written before implementation, using real file/process identity integration on Linux and a fake function-hook engine for terminal behavior.
No new Lanterns test invokes a model or Herdr lifecycle operation.
The [captured evidence](../evidence/lanterns-2026-10-24.txt) records red unit tests, green focused checks, the actual function-hook engine test and a manual generated workspace.
The CLI integration tests were added after the unit implementations and passed first; no red-first claim is made for those integration tests.
