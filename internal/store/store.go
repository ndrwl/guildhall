// Package store is a Director copy's durable record store (R1.17).
//
// One bbolt file holds every record for one Director copy, keyed by repository id.
// Exactly one process writes a store at a time: Open takes bbolt's exclusive
// kernel file lock for a writer and a shared lock for a read-only open, waiting
// at most Options.LockTimeout before returning ErrLocked.
// The kernel releases the lock when the holding process exits, including when it is killed.
//
// Every mutation names a caller-chosen mutation id and runs in one bbolt
// transaction that writes the records and a Receipt together, so after a crash
// either both are durable or neither is.
// Replaying a mutation id with the same operation and request returns the
// original receipt and changes nothing, even if later mutations changed the
// records since; reusing it for a different request returns ErrMutationConflict
// and changes nothing.
// A failed mutation writes nothing, so its id stays free.
//
// Large documents are not stored: a DocumentRef records a file's relative path and sha256.
// See docs/store.md for the file layout and the full contract.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"

	"github.com/ndrwl/guildhall/internal/digest"
)

// SchemaVersion is the newest store schema this Guildhall reads and the one it writes.
const SchemaVersion = 1

// DefaultLockTimeout bounds how long Open waits for another process's lock.
const DefaultLockTimeout = 2 * time.Second

var (
	// ErrLocked reports that another open store holds the file lock.
	ErrLocked = errors.New("store is locked by another open store")
	// ErrNewerSchema reports a store written by a newer Guildhall; it is refused rather than guessed at.
	ErrNewerSchema = errors.New("store schema is newer than this Guildhall supports")
	// ErrNotStore reports a file that is not a Guildhall store.
	ErrNotStore = errors.New("file is not a Guildhall store")
	// ErrMutationConflict reports a mutation id already used for a different request.
	ErrMutationConflict = errors.New("mutation id was already used for a different request")
	// ErrNotFound reports a missing record.
	ErrNotFound = errors.New("record not found")
	// ErrExists reports a record that already exists and may not be replaced.
	ErrExists = errors.New("record already exists")
	// ErrInvalid reports an invalid request or a transition the record does not allow.
	ErrInvalid = errors.New("invalid store request")
	// ErrClosed reports use of a closed store.
	ErrClosed = errors.New("store is closed")
	// ErrReadOnly reports a mutation on a store opened read-only.
	ErrReadOnly = errors.New("store is open read-only")
)

// testHookBeforeCommit, when set by tests, runs after a mutation's writes and
// before its transaction commits; an error rolls the transaction back.
var testHookBeforeCommit func() error

// Bucket and key names in the file layout (docs/store.md).
var (
	bucketMeta     = []byte("meta")
	bucketReceipts = []byte("receipts")
	bucketRepos    = []byte("repositories")
	keyFormat      = []byte("format")
	keySchema      = []byte("schema_version")
	formatName     = []byte("guildhall-store")
)

// Options controls Open.
type Options struct {
	// ReadOnly opens an existing store without creating, initializing or writing it.
	ReadOnly     bool
	RequireStore bool
	// LockTimeout bounds the wait for another process's lock; zero means DefaultLockTimeout.
	LockTimeout time.Duration
	// Now stamps receipts; nil means time.Now. Times are stored in UTC.
	Now func() time.Time
}

// Store is an open Director store. Its methods are safe for concurrent use.
type Store struct {
	db  *bolt.DB
	now func() time.Time
}

// Receipt records one committed mutation.
type Receipt struct {
	MutationID string `json:"mutation_id"`
	// Operation names the mutation method, such as "create-task".
	Operation string `json:"operation"`
	// RequestDigest is the sha256 of the operation and request; replays must match it.
	RequestDigest string `json:"request_digest"`
	Repository    string `json:"repository"`
	TaskID        string `json:"task_id"`
	// Attempt is the attempt number StartAttempt assigned, or the one FinishAttempt finished.
	Attempt int `json:"attempt,omitempty"`
	// Record names the record the mutation wrote within its task: "task", "attempt/<step>/<number>",
	// "document/<name>", "pane", "decision/<id>" or "message/<id>" (docs/store.md).
	// It is empty for mutations outside a task and for receipts committed before it was recorded.
	Record string `json:"record,omitempty"`
	// Sequence orders every committed mutation in the store, starting at 1.
	Sequence    uint64    `json:"sequence"`
	CommittedAt time.Time `json:"committed_at"`
}

// Open opens the store at path, creating and initializing it unless opts.ReadOnly or opts.RequireStore is set.
// It refuses a file that is not a Guildhall store (ErrNotStore) or has a newer schema (ErrNewerSchema).
func Open(path string, opts Options) (*Store, error) {
	timeout := opts.LockTimeout
	if timeout <= 0 {
		timeout = DefaultLockTimeout
	}
	if opts.ReadOnly && !opts.RequireStore {
		// bbolt would create nothing either, but report a missing file plainly.
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
	}
	boltOpts := &bolt.Options{Timeout: timeout, ReadOnly: opts.ReadOnly}
	if opts.RequireStore {
		boltOpts.NoFreelistSync = true
		boltOpts.OpenFile = func(name string, flag int, mode os.FileMode) (*os.File, error) {
			f, err := os.OpenFile(name, flag&^os.O_CREATE, mode)
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("%w: %w", ErrNotStore, err)
			}
			if err != nil {
				return nil, err
			}
			info, err := f.Stat()
			if err == nil && info.Size() == 0 {
				err = ErrNotStore
			}
			if err != nil {
				_ = f.Close()
				return nil, err
			}
			return f, nil
		}
	}
	db, err := bolt.Open(path, 0o600, boltOpts)
	switch {
	case errors.Is(err, berrors.ErrTimeout):
		return nil, fmt.Errorf("store: %s: %w", path, ErrLocked)
	case errors.Is(err, berrors.ErrInvalid), errors.Is(err, berrors.ErrVersionMismatch), errors.Is(err, berrors.ErrChecksum):
		return nil, fmt.Errorf("store: %s: %w: %w", path, ErrNotStore, err)
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("store: %w", err)
	case err != nil:
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if opts.ReadOnly || opts.RequireStore {
		err = db.View(func(tx *bolt.Tx) error { return checkMeta(tx) })
	} else {
		err = db.Update(initMeta)
	}
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: %s: %w", path, err)
	}
	db.NoFreelistSync = false
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}, nil
}

// initMeta initializes an empty file and checks an existing one.
func initMeta(tx *bolt.Tx) error {
	if tx.Bucket(bucketMeta) == nil {
		empty := true
		_ = tx.ForEach(func([]byte, *bolt.Bucket) error { empty = false; return nil })
		if !empty {
			return ErrNotStore
		}
		meta, err := tx.CreateBucket(bucketMeta)
		if err != nil {
			return err
		}
		if err := meta.Put(keyFormat, formatName); err != nil {
			return err
		}
		if err := meta.Put(keySchema, []byte(strconv.Itoa(SchemaVersion))); err != nil {
			return err
		}
		for _, name := range [][]byte{bucketReceipts, bucketRepos} {
			if _, err := tx.CreateBucket(name); err != nil {
				return err
			}
		}
	}
	return checkMeta(tx)
}

// checkMeta refuses files missing required buckets or supported Guildhall metadata.
func checkMeta(tx *bolt.Tx) error {
	for _, name := range [][]byte{bucketMeta, bucketReceipts, bucketRepos} {
		if tx.Bucket(name) == nil {
			return ErrNotStore
		}
	}
	_, err := schemaVersion(tx)
	return err
}

func schemaVersion(tx *bolt.Tx) (int, error) {
	meta := tx.Bucket(bucketMeta)
	if meta == nil || string(meta.Get(keyFormat)) != string(formatName) {
		return 0, ErrNotStore
	}
	v, err := strconv.Atoi(string(meta.Get(keySchema)))
	switch {
	case err != nil || v < 1:
		return 0, fmt.Errorf("%w: invalid schema_version %q", ErrNotStore, meta.Get(keySchema))
	case v > SchemaVersion:
		return v, fmt.Errorf("%w: schema_version %d (supported: %d)", ErrNewerSchema, v, SchemaVersion)
	}
	return v, nil
}

// Close waits for in-flight calls to finish, then releases the file lock.
// It is safe to call more than once; later calls on the store return ErrClosed.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("store: close: %w", err)
	}
	return nil
}

func (s *Store) view(fn func(tx *bolt.Tx) error) error {
	return storeErr(s.db.View(fn))
}

func storeErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, berrors.ErrDatabaseNotOpen):
		return ErrClosed
	case errors.Is(err, berrors.ErrDatabaseReadOnly), errors.Is(err, berrors.ErrTxNotWritable):
		return ErrReadOnly
	}
	return err
}

var mutationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// mutate runs apply and records its receipt in one transaction, or returns the
// original receipt when mutationID was already committed for the same request.
// apply fills the receipt's record fields and may read its sequence.
func (s *Store) mutate(mutationID, op string, req any, apply func(tx *bolt.Tx, r *Receipt) error) (Receipt, error) {
	if !mutationIDPattern.MatchString(mutationID) {
		return Receipt{}, fmt.Errorf("store: %w: mutation id %q must match %s", ErrInvalid, mutationID, mutationIDPattern)
	}
	body, err := json.Marshal(struct {
		Op  string `json:"op"`
		Req any    `json:"req"`
	}{op, req})
	if err != nil {
		return Receipt{}, fmt.Errorf("store: %w", err)
	}
	requestDigest := string(digest.Of(body))

	var out Receipt
	err = s.db.Update(func(tx *bolt.Tx) error {
		receipts := tx.Bucket(bucketReceipts)
		if prior := receipts.Get([]byte(mutationID)); prior != nil {
			if err := json.Unmarshal(prior, &out); err != nil {
				return fmt.Errorf("receipt %q: %w", mutationID, err)
			}
			if out.Operation != op || out.RequestDigest != requestDigest {
				return fmt.Errorf("%w: %q was %s", ErrMutationConflict, mutationID, out.Operation)
			}
			return nil
		}
		// The sequence is taken first so apply can record it; a failed apply rolls it back.
		seq, err := receipts.NextSequence()
		if err != nil {
			return err
		}
		r := Receipt{MutationID: mutationID, Operation: op, RequestDigest: requestDigest, Sequence: seq}
		if err := apply(tx, &r); err != nil {
			return err
		}
		r.CommittedAt = s.now().UTC()
		if err := putJSON(receipts, []byte(mutationID), r); err != nil {
			return err
		}
		if testHookBeforeCommit != nil {
			if err := testHookBeforeCommit(); err != nil {
				return err
			}
		}
		out = r
		return nil
	})
	if err != nil {
		return Receipt{}, fmt.Errorf("store: %s %s: %w", op, mutationID, storeErr(err))
	}
	return out, nil
}

// Receipt returns the receipt committed for mutationID.
func (s *Store) Receipt(mutationID string) (Receipt, error) {
	var r Receipt
	err := s.view(func(tx *bolt.Tx) error {
		return getJSON(tx.Bucket(bucketReceipts), []byte(mutationID), &r)
	})
	if err != nil {
		return Receipt{}, fmt.Errorf("store: receipt %q: %w", mutationID, err)
	}
	return r, nil
}

func putJSON(b *bolt.Bucket, key []byte, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return b.Put(key, data)
}

func getJSON(b *bolt.Bucket, key []byte, v any) error {
	if b == nil {
		return ErrNotFound
	}
	data := b.Get(key)
	if data == nil {
		return ErrNotFound
	}
	return decode(key, data, v)
}
