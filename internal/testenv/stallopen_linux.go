//go:build linux

package testenv

import (
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// StallOpens creates path empty and makes a writable open of it block for about hold: a second handle takes a read
// lease on the file, and the kernel holds any O_RDWR open until the lease holder drops it, which a goroutine does
// after hold. It reproduces a slow lock-file open, as antivirus filter drivers cause on Windows. It skips the test
// when the filesystem or the process cannot take a lease.
func StallOpens(t *testing.T, path string, hold time.Duration) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unix.FcntlInt(r.Fd(), unix.F_SETLEASE, unix.F_RDLCK); err != nil {
		_ = r.Close()
		t.Skipf("cannot take a read lease to stall the open: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		// timing:allow the stall must outlast the caller's deadline; its length only sets how long the open waits.
		time.Sleep(hold)
		_, _ = unix.FcntlInt(r.Fd(), unix.F_SETLEASE, unix.F_UNLCK)
		_ = r.Close()
	}()
	t.Cleanup(func() { <-done })
}
