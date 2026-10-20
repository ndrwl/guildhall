package adapter

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type failingWorker struct{}

func (failingWorker) stop(context.Context) error { return errors.New("termination failed") }
func (failingWorker) close() error               { return nil }

func TestOwnedWorkerFailedStopRejectsLateResult(t *testing.T) {
	for _, established := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-ownership", true: "termination-failure"}[established], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "result.json")
			listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			id := Identity{TaskID: "task", AttemptID: "attempt"}
			o := &OwnedWorker{spec: Spec{Identity: id, ResultFile: path}, listener: listener}
			defer func() {
				if err := o.Close(); err != nil {
					t.Errorf("close owned worker: %v", err)
				}
			}()
			if established {
				o.worker = failingWorker{}
			}
			if err := o.Stop(context.Background()); err == nil {
				t.Fatal("claimed termination without proof")
			}
			if _, err := ReadResult(path, id); !errors.Is(err, ErrAttemptCancelled) {
				t.Fatalf("late read = %v", err)
			}
			if err := WriteResult(path, Result{Identity: id, Status: StatusSucceeded, Summary: "late", Outputs: []Output{}}); !errors.Is(err, ErrAttemptCancelled) {
				t.Fatalf("late write = %v", err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("late result exists: %v", err)
			}
		})
	}
}

func TestOwnedWorkerUnsupported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows supplies native handle stopping")
	}
	if _, err := NewOwnedWorker(Spec{}, ""); !errors.Is(err, ErrStopUnsupported) {
		t.Fatalf("unsupported platform = %v", err)
	}
}
