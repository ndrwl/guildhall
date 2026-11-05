package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ndrwl/guildhall/internal/backup"
)

// supervisorLogFile is the log, in the copy's state directory, of the session supervisor's background jobs: the
// daily prune, the workspace close sweep, the session backup, peer deliveries and the Steward's scheduled collect
// (docs/running.md#pruning). They write there, not to the supervisor's standard error, because under a session that
// is the terminal the runtime draws. It is not rotated, like the session directory's watcher logs, and not backed up.
const supervisorLogFile = backup.SupervisorLogFile

// supervisorLogMu covers each write to a supervisor log, so lines from jobs running at once do not interleave.
var supervisorLogMu sync.Mutex

// supervisorLogWriter appends each line written to it, time-stamped, to the log at path.
type supervisorLogWriter struct {
	path string
	now  func() time.Time
}

// supervisorLog is the supervisor's log in state, state being the copy-level state directory. With no state, as in
// a role start given none, there is no log and what is written is dropped, never written to the working directory.
func supervisorLog(state string) io.Writer {
	if state == "" {
		return io.Discard
	}
	return &supervisorLogWriter{path: filepath.Join(state, supervisorLogFile), now: time.Now}
}

// Write appends each line of b, prefixed with an RFC 3339 UTC time stamp as watchLog does, and ignores a log it
// cannot open, so a job's report never fails the job.
func (l *supervisorLogWriter) Write(b []byte) (int, error) {
	var out strings.Builder
	stamp := l.now().UTC().Format(time.RFC3339)
	for line := range strings.Lines(string(b)) {
		_, _ = fmt.Fprintf(&out, "%s %s\n", stamp, strings.TrimSuffix(line, "\n"))
	}
	if out.Len() == 0 {
		return len(b), nil
	}
	supervisorLogMu.Lock()
	defer supervisorLogMu.Unlock()
	_ = os.MkdirAll(filepath.Dir(l.path), 0o755)
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return len(b), nil
	}
	_, _ = f.WriteString(out.String())
	_ = f.Close()
	return len(b), nil
}
