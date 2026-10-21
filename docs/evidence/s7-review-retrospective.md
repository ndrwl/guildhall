# S7 review fixes: authorized retrospective regression evidence

## Authorization and scope

For R4, the user explicitly authorized retrofitting the regression evidence for R1–R3 by demonstrating each corrected regression failing against the pre-fix implementation and passing against the fixed implementation.
This evidence is **retrospective**: it does not establish that failing tests were executed before the fixes were originally implemented.

Only the existing corrected regressions were executed.
No production or test code was changed for this comparison.
This focused review verification does not replace the outer pipeline's test, lint or platform-validation phases.

## Compared versions

- Pre-fix production: `05b9d858747331add6ce56fac7f36936c7cdc2b4`.
- Fixed production and corrected tests: `6cd47d5380b9e8800d9676884e24dc050f47787a`.
- Runtime: `go version go1.27.2 linux/amd64`.
- The failing run used Go's build overlay to replace only `internal/launch/process.go` and `internal/interp/engine.go` with their exact pre-fix versions.
  These are the two production files changed by the fix commit.
  Both runs used the same corrected tests from the fixed commit.
  The overlay and its backing files were created and removed inside the worktree; checked-out production files were never replaced.

## Observed results

The pre-fix command exited **1** with behavioral assertion failures, not build failures.
The fixed command exited **0**.
Full captured output: [failing run](s7-review-retrospective-red.txt) and [passing run](s7-review-retrospective-green.txt).

| Finding / regression | Pre-fix observation | Fixed observation |
| --- | --- | --- |
| R1: `TestProcessDuplicateLeavesOwnerRunning` | `duplicate launch released the original owner` | Pass; original ownership and status endpoint survive the refused duplicate; explicit and repeated stops succeed |
| R2: `TestInterruptedLauncherIntake` | `second output lost on recovery`, on both backends | Pass on both backends |
| R2: `TestDirectorRulings/contested` | Starts build attempt 3 instead of waiting for Guildmaster decision `review-x2`, on both backends | Pass on both backends; contest and pause recovered |
| R2: `TestStallSignals` | Round 2 stall message is missing, on both backends | Pass on both backends |
| R3: `TestInterpreterFinishRace/*/succeeded` | Advance returns `already finished` instead of joining the identical outcome, on both backends | Pass on both backends |

The withdrawn-ruling controls and stopped/failed conflicting-outcome controls passed in both runs; these are preservation checks, not claimed pre-fix failures.

## Reproduction

From the fixed commit, run this paired comparison inside the worktree.
The first command is expected to fail; the second is expected to pass.
The recorded logs also include the Go version, platform, compared commit IDs and exit statuses.

```sh
scratch=$(mktemp -d "$PWD/.r4-retrospective.XXXXXX")
trap 'rm -rf "$scratch"' EXIT
base=05b9d858747331add6ce56fac7f36936c7cdc2b4
git show "$base:internal/launch/process.go" > "$scratch/process.go"
git show "$base:internal/interp/engine.go" > "$scratch/engine.go"
printf '{"Replace":{"%s/internal/launch/process.go":"%s/process.go","%s/internal/interp/engine.go":"%s/engine.go"}}\n' \
  "$PWD" "$scratch" "$PWD" "$scratch" > "$scratch/overlay.json"
selection='^(TestProcessDuplicateLeavesOwnerRunning|TestInterruptedLauncherIntake|TestDirectorRulings|TestStallSignals|TestInterpreterFinishRace)$'
red_status=0
go test -overlay "$scratch/overlay.json" ./internal/launch ./internal/interp \
  -run "$selection" -count=1 -v || red_status=$?
green_status=0
go test ./internal/launch ./internal/interp \
  -run "$selection" -count=1 -v || green_status=$?
test "$red_status" -eq 1 && test "$green_status" -eq 0
```
