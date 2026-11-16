# `internal/filelock`

## Contract

`filelock` is Guildhall's one exclusive kernel file lock, a leaf package that imports only the standard library and `x/sys`.
It flocks the whole file on Unix (build tag `unix`), ignoring the offset, and locks the one byte at the offset with `LockFileEx` on Windows, so a holder locking past a file's content leaves that content readable through other handles.
The operating system releases the lock when the holder closes the file or dies.

- `Lock(ctx, f, offset)` takes the lock on an open file. It always makes one nonblocking attempt, whatever the state of ctx; after a refused attempt it retries every 10 ms until it gets the lock or ctx is done, and then returns `ctx.Err()`, so `context.DeadlineExceeded` means a holder refused the lock. Any error other than a refusal is returned at once.
- `LockPath(ctx, path, offset)` fails with ctx's error, opening and creating nothing, when ctx is already done; otherwise it opens the file `O_RDWR|O_CREATE`, mode 0600, never truncating it, and calls `Lock`, closing the file on failure and returning the locked file on success. The time the open takes does not count against ctx, so a slow open never turns a free lock into a refusal ([slow lock-file open](../test-timing.md#slow-lock-file-open)).
- `Wait(f, offset)` takes the same lock, blocking until it is free.
- `Unlock(f, offset)` releases the lock before the file is closed: on Windows closing a handle does not release its locks at once, so a holder that hands the lock on promptly unlocks first.

A refusal is `EWOULDBLOCK` on Unix and `ERROR_LOCK_VIOLATION` on Windows, compared with `errors.Is`.
`ERROR_IO_PENDING` also counts as a refusal on Windows: the handles are synchronous, so it should not occur, and if it does, retrying until ctx ends is safer than failing a caller who could have had the lock.
One implementation and no interface, like [`atomicfile`](atomicfile.md).

Consumers: [`artisan`](artisan.md)'s `LockPathAt`, `LockPath` and `LockAttempt`, which are `LockPath`, and so the Steward's private lock; the launcher's attempt lock, through `Wait`; and [`release`](release.md)'s home lock, through `Lock` and `Unlock` on a file it opens itself.

## Tests

`filelock_test.go`, moved from `artisan`'s `lock_test.go` (d245), runs on every CI OS: a file held through `LockPath` at a high offset refuses a second handle by its deadline, still reads, through another handle, bytes the holder wrote at offset 0, and is free once the holder closes it; locking keeps existing content; and a cancelled context returns `context.Canceled` and leaves the path absent.
`TestLockDoneCtxTriesOnce` checks that `Lock` with a context already done still takes a free lock and refuses a held one; `TestUnlockFreesTheLock` that a second handle takes the lock at once after `Unlock`, while the holder's file is still open; and `TestWaitBlocksUntilTheHolderCloses` that `Wait` is still blocked while another handle holds the lock and returns with it once the holder closes.
`filelock_slowopen_linux_test.go`'s `TestLockPathSlowOpenStillTakesAFreeLock`, Linux only, stalls the open of a free lock past a 100ms deadline with [`testenv.StallOpens`](testenv.md#contract) and wants the locked file, not `context.DeadlineExceeded`; it skips when no read lease can be taken.
