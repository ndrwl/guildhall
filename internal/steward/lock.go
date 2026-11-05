package steward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
)

// lockName is the file of the private config directory's writer lock, which a rollout apply, a collection round, a
// takeover apply or abandon, a secrets set or apply, and a machine retirement or its rotated mark take, and
// privateLock alone names. The lock is a kernel lock on the file, released when its owner closes it or dies; the file
// stays in place and only holds the owner record.
const lockName = ".rollout.lock"

// maxLockRecord bounds how much of a lock file readLockOwner reads.
const maxLockRecord = 4096

// ErrBusy reports a step that found .rollout.lock held by a live process, a rollout, a collection or a takeover,
// and changed nothing. A *BusyError matches it with errors.Is and names the lock's owner.
var ErrBusy = errors.New("busy: the private config's .rollout.lock is held by a rollout or another collection")

// LockOwner is the owner record privateLock writes into .rollout.lock: the process that took it, by PID and OS
// start token (artisan.Identity), its command line, and when it took the lock by its caller's clock. A zero PID is
// an unknown owner: a held lock with no record, a record not yet or only partly written, or one being released;
// its At is then the file's modification time.
type LockOwner struct {
	PID     int       `json:"pid"`
	Token   string    `json:"token,omitempty"`
	Command string    `json:"command,omitempty"`
	At      time.Time `json:"at"`
}

// Known reports whether the record names a process.
func (o LockOwner) Known() bool { return o.PID > 0 }

// String names the owner: its PID and command, or "an unknown owner".
func (o LockOwner) String() string {
	if !o.Known() {
		return "an unknown owner"
	}
	if o.Command == "" {
		return fmt.Sprintf("pid %d", o.PID)
	}
	return fmt.Sprintf("pid %d (%s)", o.PID, o.Command)
}

// BusyError is the error privateLock returns when .rollout.lock is held. It matches both ErrBusy and fs.ErrExist
// with errors.Is, and its text names the lock path and its owner.
type BusyError struct {
	Path  string
	Owner LockOwner
}

func (e *BusyError) Error() string {
	s := fmt.Sprintf("busy: %s is held by %s", e.Path, e.Owner)
	if !e.Owner.At.IsZero() {
		s += " since " + e.Owner.At.UTC().Format(time.RFC3339)
	}
	return s
}

// Is makes a *BusyError match ErrBusy and fs.ErrExist.
func (e *BusyError) Is(target error) bool { return target == ErrBusy || target == fs.ErrExist }

// lockOffset is where TakePrivateLock locks .rollout.lock on Windows: far past any owner record (maxLockRecord), so
// a refused reader can still read the record through its own handle. Unix flocks the whole file.
const lockOffset = 1 << 30

// lockWait bounds how long TakePrivateLock waits for a held lock before reporting it busy.
const lockWait = 100 * time.Millisecond

// privateLock takes .rollout.lock in dir with this process's owner record, stamped now; see TakePrivateLock.
func privateLock(dir string, now time.Time) (release func(), err error) {
	owner := LockOwner{PID: os.Getpid(), Command: strings.Join(os.Args, " "), At: now.UTC()}
	if id, err := artisan.Self(); err == nil {
		owner.PID, owner.Token = id.PID, id.Token
	}
	return TakePrivateLock(dir, owner)
}

// TakePrivateLock takes the kernel lock on .rollout.lock in dir (artisan.LockPathAt), creating the file if needed,
// and writes owner's record into it through the locked handle. The OS releases the lock when its process dies, so a
// crashed writer never blocks the next one; the file itself is not the lock and stays in place for good, holding the
// last owner's record. A held lock is a *BusyError naming the record, so busy means a live owner, possibly a hung
// one, never a crashed one. A reader can briefly see an empty record, just after a holder takes the lock or while it
// releases it, and then names an unknown owner. A failed record write closes the file, freeing the lock.
//
// The returned release truncates the record (best effort) and closes the file, freeing the lock. It never removes
// the file: a waiter could then lock the unlinked file while a newcomer locks a new one, and both would write.
// privateLock is the only taker; TakePrivateLock is exported so internal/cli tests can hold a real lock.
func TakePrivateLock(dir string, owner LockOwner) (release func(), err error) {
	path := filepath.Join(dir, lockName)
	ctx, cancel := context.WithTimeout(context.Background(), lockWait)
	f, err := artisan.LockPathAt(ctx, path, lockOffset)
	cancel()
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, &BusyError{Path: path, Owner: readLockOwner(path)}
	} else if err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}
	b, err := json.Marshal(owner)
	if err == nil {
		err = f.Truncate(0)
	}
	if err == nil {
		err = writeLockRecord(f, append(b, '\n'))
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock: %w", err)
	}
	return func() {
		_ = f.Truncate(0)
		_ = f.Close()
	}, nil
}

// writeLockRecord writes the owner record b at the start of the locked, truncated file f; tests replace it to fail
// the write.
var writeLockRecord = func(f *os.File, b []byte) error {
	_, err := f.WriteAt(b, 0)
	return err
}

// readLockOwner reads the owner record of the lock at path. An empty, free-text or partly written file is an unknown
// owner whose At is the file's modification time. It only reads the file, never rewriting or removing it.
func readLockOwner(path string) LockOwner {
	var o LockOwner
	f, err := os.Open(path)
	if err != nil {
		return o
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxLockRecord))
	if err != nil || json.Unmarshal(b, &o) != nil || !o.Known() {
		o = LockOwner{}
		if fi, err := f.Stat(); err == nil {
			o.At = fi.ModTime().UTC()
		}
	}
	return o
}
