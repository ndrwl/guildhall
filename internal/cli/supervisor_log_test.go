package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// testSupervisorLog is a test's view of the supervisor.log in state: what the supervisor's jobs wrote to it.
type testSupervisorLog struct {
	t     *testing.T
	state string
}

func newTestSupervisorLog(t *testing.T) *testSupervisorLog {
	return &testSupervisorLog{t: t, state: t.TempDir()}
}

// writer is the supervisor's log writer for the test's state.
func (l *testSupervisorLog) writer() *supervisorLogWriter {
	return supervisorLog(l.state).(*supervisorLogWriter)
}

// String is the log's contents so far, empty when nothing was written.
func (l *testSupervisorLog) String() string {
	l.t.Helper()
	b, err := os.ReadFile(filepath.Join(l.state, supervisorLogFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		l.t.Fatal(err)
	}
	return string(b)
}

// Reset empties the log.
func (l *testSupervisorLog) Reset() {
	l.t.Helper()
	if err := os.Remove(filepath.Join(l.state, supervisorLogFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		l.t.Fatal(err)
	}
}

var supervisorLogLine = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ guildhall: job (\d+): line (\d+) of a long report x+$`)

// TestSupervisorLog: jobs writing at once produce only whole, time-stamped lines, each write's lines split into
// one stamped line apiece; a log that cannot be opened is ignored.
func TestSupervisorLog(t *testing.T) {
	l := newTestSupervisorLog(t)
	w := l.writer()
	w.now = func() time.Time { return time.Date(2026, 11, 5, 9, 0, 0, 0, time.FixedZone("x", 3600)) }
	const jobs, writes = 8, 50
	pad := strings.Repeat("x", 4096)
	var wg sync.WaitGroup
	for j := range jobs {
		wg.Go(func() {
			for i := range writes {
				// Two lines per write, the second without its newline.
				_, _ = fmt.Fprintf(w, "guildhall: job %d: line %d of a long report %s\nguildhall: job %d: line %d of a long report %s", j, 2*i, pad, j, 2*i+1, pad)
			}
		})
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSuffix(l.String(), "\n"), "\n")
	if len(lines) != jobs*writes*2 {
		t.Fatalf("%d lines, want %d", len(lines), jobs*writes*2)
	}
	for _, line := range lines {
		if !supervisorLogLine.MatchString(line) || !strings.HasPrefix(line, "2026-11-05T08:00:00Z ") {
			t.Fatalf("line %.80q… is not one whole time-stamped line", line)
		}
	}
	// Windows has no Unix permission bits: Go reports a writable file as 0666 there.
	if info, err := os.Stat(filepath.Join(l.state, supervisorLogFile)); err != nil {
		t.Errorf("stat log: %v", err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("log mode = %v; want 0600", info.Mode().Perm())
	}

	// A state path that is a file: the log cannot be opened, and the write is ignored.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := fmt.Fprintf(supervisorLog(file), "guildhall: lost\n"); err != nil || n != len("guildhall: lost\n") {
		t.Errorf("write to an unwritable log = %d, %v; want it ignored", n, err)
	}
}
