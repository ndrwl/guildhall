# D46. A CI runner's required tools turn their skips into failures

Tests that need an installed shell or Windows skip by name when the host lacks it, and `go test` without `-v` prints nothing for a skip, so a CI log cannot show that a case ran where it was meant to.
Each CI job now sets `GUILDHALL_CI_REQUIRE` to the tools and platform its runner supplies, and those skips go through `internal/testenv`, which fails the test when the missing name is required.
Local runs leave it unset and skip as before.
CI is not switched to `go test -v`: a verbose log shows a skip only to someone who reads it, while a failure stops the job.
Only the skips for an absent `pwsh`, `powershell` or `ksh` and the Windows-only cases are covered; the cgroup, symbolic-link, root, path-padding and Go-toolchain skips depend on host configuration rather than a tool the runner promises, and stay plain skips.
The requirable names are a fixed list, so a misspelt requirement fails every runner instead of quietly requiring nothing.
