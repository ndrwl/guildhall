package steward

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Like scp's ssh child, this child inherits the output pipes and outlives
// cancellation of the parent. Waiting for those pipes must not defeat Timeout.
func TestTransportTimeoutWithChild(t *testing.T) {
	for _, operation := range []string{"fetch", "put"} {
		t.Run(operation, func(t *testing.T) {
			s := newStub(t)
			if runtime.GOOS == "windows" {
				// Windows kills only the parent; let the bounded child exit
				// before TempDir cleanup removes its executable.
				defer time.Sleep(2 * time.Second)
			}
			t.Setenv(stubHangEnv, "hung:")
			t.Setenv("GUILDHALL_STUB_SCP_CHILD", "1")
			transport := s.transport()
			transport.Timeout = 250 * time.Millisecond
			remote := Remote{Alias: "hung", Path: "/file"}
			local := filepath.Join(t.TempDir(), "file")
			start := time.Now()
			var err error
			if operation == "fetch" {
				err = transport.Fetch(context.Background(), remote, local)
			} else {
				err = transport.Put(context.Background(), local, remote)
			}
			elapsed := time.Since(start)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected deadline exceeded, got %v", err)
			}
			if elapsed > time.Second {
				t.Fatalf("250ms timeout took %s with child holding output pipes", elapsed)
			}
		})
	}
}
