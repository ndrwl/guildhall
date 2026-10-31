package steward

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/testenv"
)

// TestTransportTimeoutWithChild covers cancelling an scp or ssh run whose child holds its output pipes: the shared
// cancellation kills the whole process group on Unix, and the direct process plus WaitDelay elsewhere.
func TestTransportTimeoutWithChild(t *testing.T) {
	for _, operation := range []string{"fetch", "put", "ssh run"} {
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
				switch operation {
				case "fetch":
					done <- transport.Fetch(ctx, remote, local)
				case "put":
					done <- transport.Put(ctx, local, remote)
				default:
					_, err := s.runner().Run(ctx, remote.Alias, ReloadCommand(DefaultGuildhall, "/home/guild/directors/tools"))
					done <- err
				}
			}()
			readyTimeout := time.NewTimer(testenv.Ceiling(10 * time.Second))
			defer readyTimeout.Stop()
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for {
				_, err := os.Stat(filepath.Join(s.root, "child-ready"))
				if err == nil {
					data, err := os.ReadFile(filepath.Join(s.root, "child-ready.pid"))
					if err != nil {
						t.Fatal(err)
					}
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
			case <-time.After(testenv.Ceiling(10 * time.Second)):
				t.Fatal("transport did not return after cancellation with child holding output pipes")
			}
		})
	}
}
