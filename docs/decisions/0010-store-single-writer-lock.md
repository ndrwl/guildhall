# D10. Store single-writer lock

The single-writer rule uses bbolt's own file lock and no separate lock file or locking package.
An exclusive lock on the database file for a writer and a shared lock for a read-only open, with a bounded wait, already give the contract the store needs: one writer per file, released by the kernel when the process dies.
The landed gomate `internal/lock` package wraps `github.com/gofrs/flock` for a separate lock file; it would add a dependency and a second lock without a behaviour the store needs, so no gomate code is copied and no notice is added.
