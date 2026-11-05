package steward

import (
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

// lockName is the private config directory's writer lock, which a rollout apply, a collection round, a takeover
// apply or abandon, a secrets set or apply, and a machine retirement or its rotated mark take, and privateLock alone
// names.
const lockName = ".rollout.lock"

// maxLockRecord bounds how much of a lock file readLockOwner reads.
const maxLockRecord = 4096

// ErrBusy reports a step that found .rollout.lock held, by a rollout, a collection or a takeover, and changed
// nothing. A *BusyError matches it with errors.Is and names the lock's owner.
var ErrBusy = errors.New("busy: the private config's .rollout.lock is held by a rollout or another collection")

// LockOwner is the owner record privateLock writes into .rollout.lock: the process that took it, by PID and OS
// start token (artisan.Identity), its command line, and when it took the lock by its caller's clock. A zero PID is
// an unknown owner: a lock with no record, as an older binary leaves, or a record not yet or only partly written;
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

// privateLock takes .rollout.lock in dir exclusively and writes this process's owner record into it, stamped now.
// It returns the lock's release, which removes the file. A held lock is a *BusyError, and the lock is left as it is:
// recovery from a lock whose owner stopped is manual (docs/steward.md#publication-and-writer-containment). A failed
// record write or close removes the file it created, so privateLock never leaves an ownerless lock of its own.
func privateLock(dir string, now time.Time) (release func(), err error) {
	path := filepath.Join(dir, lockName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, &BusyError{Path: path, Owner: readLockOwner(path)}
		}
		return nil, fmt.Errorf("lock: %w", err)
	}
	owner := LockOwner{PID: os.Getpid(), Command: strings.Join(os.Args, " "), At: now.UTC()}
	if id, err := artisan.Self(); err == nil {
		owner.PID, owner.Token = id.PID, id.Token
	}
	b, err := json.Marshal(owner)
	if err == nil {
		err = writeLockRecord(f, append(b, '\n'))
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("lock: %w", err)
	}
	return func() { _ = os.Remove(path) }, nil
}

// writeLockRecord writes the owner record b into the new lock f; tests replace it to fail the write.
var writeLockRecord = func(f *os.File, b []byte) error {
	_, err := f.Write(b)
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
