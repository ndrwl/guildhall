package artisantest

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
)

// errSharingViolation is the error Windows CI run 41187907325 reported reading
// a worker record another process held open.
var errSharingViolation = errors.New(`open C:\Temp\002\worker-7020.json: The process cannot access the file because it is being used by another process`)

// recordingTB stands in for the case's testing.T: it records failures, and a
// fatal one ends the calling goroutine as testing.T does.
type recordingTB struct {
	testing.TB
	mu     sync.Mutex
	fatal  bool
	failed []string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) fail(fatal bool, msg string) {
	r.mu.Lock()
	r.failed = append(r.failed, msg)
	r.fatal = r.fatal || fatal
	r.mu.Unlock()
}

func (r *recordingTB) Error(args ...any)                 { r.fail(false, fmt.Sprint(args...)) }
func (r *recordingTB) Errorf(format string, args ...any) { r.fail(false, fmt.Sprintf(format, args...)) }
func (r *recordingTB) Fatal(args ...any) {
	r.fail(true, fmt.Sprint(args...))
	runtime.Goexit()
}
func (r *recordingTB) Fatalf(format string, args ...any) {
	r.fail(true, fmt.Sprintf(format, args...))
	runtime.Goexit()
}

// swapInventory makes read the harness's inventory reader for t.
func swapInventory(t *testing.T, read func(string) ([]Entry, error)) {
	t.Helper()
	old := readInventory
	readInventory = read
	t.Cleanup(func() { readInventory = old })
}

// callEntriesIn runs entriesIn on a recordingTB in its own goroutine, as a
// test case would, so a fatal failure ends only that goroutine.
func callEntriesIn(t *testing.T, dir, role string, n int) ([]Entry, *recordingTB) {
	tb := &recordingTB{TB: t}
	var got []Entry
	done := make(chan struct{})
	go func() {
		defer close(done)
		got = entriesIn(tb, dir, role, n)
	}()
	<-done
	return got, tb
}

// TestEntriesInPollsThroughReadErrors is d111's regression: a transient read
// error, such as a Windows sharing violation on a record another process
// holds open, is not yet an entry; it must not fail the case at once.
func TestEntriesInPollsThroughReadErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		want := Entry{Identity: artisan.Identity{PID: 7020, Token: "t-7020"}, Role: "worker"}
		if err := Record(dir, want); err != nil {
			t.Fatal(err)
		}
		calls := 0
		swapInventory(t, func(d string) ([]Entry, error) {
			if calls++; calls <= 3 {
				return nil, errSharingViolation
			}
			return Inventory(d)
		})
		got, tb := callEntriesIn(t, dir, "worker", 1)
		if len(tb.failed) != 0 {
			t.Fatalf("entriesIn failed on a transient read error: %q", tb.failed)
		}
		if len(got) != 1 || got[0] != want {
			t.Errorf("entriesIn = %v, want [%v]", got, want)
		}
		if calls != 4 {
			t.Errorf("Inventory read %d times, want 4", calls)
		}
	})
}

// TestEntriesInFailsAtOnceOnAnInvalidRecord keeps a malformed record, which is
// structural and not a race, fatal on first sight.
func TestEntriesInFailsAtOnceOnAnInvalidRecord(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		invalid := fmt.Errorf("%w: worker-1.json holds worker-2.json", artisan.ErrInvalidProcessRecord)
		swapInventory(t, func(string) ([]Entry, error) {
			calls++
			return nil, invalid
		})
		start := time.Now()
		_, tb := callEntriesIn(t, t.TempDir(), "worker", 1)
		if !tb.fatal || len(tb.failed) != 1 || !strings.Contains(tb.failed[0], invalid.Error()) {
			t.Errorf("entriesIn failures %q (fatal %v), want one fatal naming %q", tb.failed, tb.fatal, invalid)
		}
		if calls != 1 || time.Since(start) != 0 {
			t.Errorf("entriesIn read %d times over %v before failing, want once at once", calls, time.Since(start))
		}
	})
}

// TestEntriesInNamesAPersistentReadErrorAtTheDeadline bounds the poll: a read
// error that never clears fails the case at deadline, naming the error.
func TestEntriesInNamesAPersistentReadErrorAtTheDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		swapInventory(t, func(string) ([]Entry, error) { return nil, errSharingViolation })
		start := time.Now()
		_, tb := callEntriesIn(t, t.TempDir(), "worker", 1)
		if !tb.fatal || len(tb.failed) != 1 || !strings.Contains(tb.failed[0], errSharingViolation.Error()) {
			t.Fatalf("entriesIn failures %q (fatal %v), want one fatal naming %q", tb.failed, tb.fatal, errSharingViolation)
		}
		if waited := time.Since(start); waited < deadline {
			t.Errorf("entriesIn gave up after %v, want the full %v", waited, deadline)
		}
	})
}

// TestInventoryOfRetriesAFailedRead covers the empty-inventory checks: a read
// error is retried, not taken as an empty inventory or as a failure.
func TestInventoryOfRetriesAFailedRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		swapInventory(t, func(string) ([]Entry, error) {
			if calls++; calls <= 2 {
				return nil, errSharingViolation
			}
			return []Entry{{Identity: artisan.Identity{PID: 9, Token: "t-9"}, Role: "worker"}}, nil
		})
		tb := &recordingTB{TB: t}
		var got []Entry
		done := make(chan struct{})
		go func() { defer close(done); got = inventoryOf(tb, t.TempDir()) }()
		<-done
		if len(tb.failed) != 0 || len(got) != 1 {
			t.Errorf("inventoryOf = %v, failures %q; want the one entry and no failure", got, tb.failed)
		}
		swapInventory(t, func(string) ([]Entry, error) { return nil, errSharingViolation })
		tb = &recordingTB{TB: t}
		done = make(chan struct{})
		go func() { defer close(done); got = inventoryOf(tb, t.TempDir()) }()
		<-done
		if !tb.fatal || len(tb.failed) != 1 || !strings.Contains(tb.failed[0], errSharingViolation.Error()) {
			t.Errorf("inventoryOf failures %q (fatal %v), want one fatal naming %q", tb.failed, tb.fatal, errSharingViolation)
		}
	})
}
