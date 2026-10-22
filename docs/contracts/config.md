# `internal/config`

## Contract

`Parse` refuses unknown keys, newer schemas and credentials, applies defaults and validates (`docs/configuration.md`).
Repository paths follow the owner-machine contract (D11): POSIX-rooted and rooted Windows drive-letter paths are accepted on every host; relative, drive-relative and backslash-prefixed UNC paths are refused.
Duplicate comparison normalizes dot segments and, for drive paths, separators and drive-letter case independently of the parsing host.
`EncodeManaged` and `ParseCombined` frame and verify the separate managed file and refuse inline managed settings beside it (D14).
`Load` follows the filesystem fallback and error rules in [configuration](../configuration.md).

`Profile` and `CheckProfiles` refuse an unknown profile name (`ErrUnknownProfile`).
`StallInterval` is the effective `stall_interval` [setting](../configuration.md#settings) as a duration, `DefaultStallInterval` (15 minutes) when unset and zero for `0s`; `Parse` refuses a managed or local value that is not a nonnegative Go duration, and `StallInterval` returns that error for a config built without `Parse`.
`ChecksRefresh` is the effective `checks_refresh` setting the same way, `DefaultChecksRefresh` (1 minute) when unset and zero for `0s`.
`GuardRails` returns the [guard rails](../director.md#guard-rails): the listed repositories in config order, their merge policies, and the effective `max_running_attempts` and `max_attempts_per_day` settings, `DefaultMaxRunningAttempts` (4) and `DefaultMaxAttemptsPerDay` (40) when unset; `Parse` refuses a managed or local cap that is not a positive whole number written plainly, and `GuardRails` returns that error, naming the key, for a config built without `Parse`.
`Quality` returns the effective [quality settings](../configuration.md#quality-settings), each its `DefaultQuality…` value when unset, with `skill_edits_need_approval` parsed for the later lesson-proposal work and read by nothing yet; `Parse` refuses a managed or local value that is not `on` or `off` for a switch, not a whole number written plainly for a budget or the audit sample, not a positive whole number for runs per task, not `off` or `on-change-and-release` for reference runs, or not a positive dollar amount with at most two decimals for the cost cap, naming the key, and refuses effective reference runs with no cost cap; `Quality` returns the same errors for a config built without `Parse`.
`ListedRepository` returns a listed repository or an error wrapping `ErrRepositoryNotListed` that names the id; every command acting on a repository finds it there.
`HerdrSession` is the effective `herdr_session` setting, `""` when unset; `Parse` refuses a managed or local value that is `default` or not a Herdr session name.
A repository's `NoCI` is its `no_ci` setting, `false` by default.
`Director` is the optional [`[director]` table](../configuration.md#the-director-table): an adapter that is an id when set, a config-relative role path, an `idle_after` that is a positive Go duration when set, and none in a Steward config; it is omitted from JSON when unset, as `idle_after` is, so a config without them keeps its digest.
`IdleAfter` returns the effective `[director] idle_after`, `DefaultIdleAfter` (5 minutes) when unset.

## Tests

`config_test.go`, `managed_test.go`, `profile_test.go`; real temporary files: `load_test.go`.
`stall_test.go` was written red first against a stub `StallInterval` ([red run](../evidence/stall-adapter-choice-2026-10-22-red.txt)): the default, local, zero, managed and overridden values, and refusal of values that are not nonnegative durations.
`checks_refresh_test.go` pins the same cases for `ChecksRefresh`, and `TestDirectorConfig` the `no_ci` default and value; they were added with the implementation and were not run red.
`herdr_session_test.go` was written red first against a stub `HerdrSession` ([red run](../evidence/director-tab-2026-10-22-red.txt)): unset, local and managed values, and refusals of `default` and malformed names.
`quality_test.go` was written red first against a stub `Quality` ([red run](../evidence/quality-rubric-2026-10-22-red.txt)): the defaults, each setting set locally, managed and overridden, a managed cost cap beside local reference runs, and the refusals of each malformed value in both sections and of reference runs with no cap; the `quality_skill_edits_need_approval` cases were added after that run and were not run red.
`director_test.go` pins the `[director]` table's parse and refusals, `idle_after` and its default among them; it was added with the implementation and was not run red.
`guard_rails_test.go` was written red first against stub `GuardRails` and `ListedRepository` ([red run](../evidence/guard-rails-2026-10-22-red.txt)): the defaults, local, managed and overridden caps, the listed ids and merge policies, refusal of caps that are zero, negative, words, fractions or padded, and the named refusal of an unlisted repository.
