# Linux containment: delegated cgroup v2, else subreaper and process group

A Linux unit is a child cgroup `guildhall-<name>` of the launcher's own cgroup when `/sys/fs/cgroup` is cgroup v2 and this user may write that cgroup and its `cgroup.procs` and it has `cgroup.kill`; otherwise it is a process group with the launcher as child subreaper ([Artisan launcher](../artisan.md#linux-detection)).
The worker is created inside the cgroup with `CLONE_INTO_CGROUP` rather than moved after it starts, so no descendant can be created outside it.
No controller is enabled, so a delegated cgroup that already holds the launcher can hold the child and `cgroup.subtree_control` is not required.
Detection reads the host's own files rather than trying to create a cgroup first, so the reason it records says which check failed; a cgroup that detection allowed but that cannot be created is an error, not a silent fallback.
Under the fallback a `setsid` escape stays reported, not contained: the subreaper makes an orphaned escape a child of the launcher, where `Escapes` reports it, but `Kill` does not reach it.
The `RecordContainment` helper provides the [attempt-directory record](../artisan.md#recording-the-containment) for later launcher integration; creating a unit does not persist it automatically.
