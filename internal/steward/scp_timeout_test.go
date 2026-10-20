package steward

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestTransportTimeoutWithChild(t *testing.T) {
	for _, operation := range []string{"fetch", "put"} {
		t.Run(operation, func(t *testing.T) {
			s := newStub(t)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(stubHangEnv, "hung:")
			t.Setenv("GUILDHALL_STUB_SCP_CHILD", executable)
			transport := s.transport()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			remote := Remote{Alias: "hung", Path: "/file"}
			local := filepath.Join(t.TempDir(), "file")
			done := make(chan error, 1)
			go func() {
				if operation == "fetch" {
					done <- transport.Fetch(ctx, remote, local)
				} else {
					done <- transport.Put(ctx, local, remote)
				}
			}()
			readyTimeout := time.NewTimer(10 * time.Second)
			defer readyTimeout.Stop()
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for {
				data, err := os.ReadFile(filepath.Join(s.root, "child-ready"))
				if err == nil {
					pid, err := strconv.Atoi(string(data))
					if err != nil {
						t.Fatal(err)
					}
					child, err := os.FindProcess(pid)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = child.Kill(); _ = child.Release() }()
					break
				}
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
				select {
				case err := <-done:
					t.Fatalf("transport returned before child readiness: %v", err)
				case <-readyTimeout.C:
					t.Fatal("child did not become ready")
				case <-tick.C:
				}
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("expected cancellation, got %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("transport did not return after cancellation with child holding output pipes")
			}
		})
	}
}
