# D28. Per-platform containment for Artisan workers

The launcher owns each worker through one containment interface with a backend per platform: a Windows Job Object that kills on close and refuses breakaway, Linux cgroup v2 when delegated otherwise subreaper plus process group, and a macOS process group ([Artisan launcher](../artisan.md#containment-unit)).
The Linux design arrives in slice L2; the process group alone is the accepted interim Linux backend in this L1 PR.
The process-group leader stays unreaped so its id cannot be reused.
A stop counts only when the unit is observed empty, because a delivered signal or a closed terminal does not prove that processes ended.
Where a platform cannot contain a descendant, such as a `setsid` escape from a Unix process group, the limit is reported and tested as an escape rather than claimed as contained.
One contract suite runs against every backend on its native runner and against the double the launcher's tests use.
