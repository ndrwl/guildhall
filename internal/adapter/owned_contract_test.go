package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/testenv"
)

// These cases pin OwnedWorker's cancellation contract on every platform: a
// stop records the attempt's cancellation before acting, a nil Stop is
// claimed only when the native handle proved termination, and a late result
// is rejected whatever happened to the process. Ownership is established
// through the real loopback handshake; only the native handle is the
// fakeNative double, whose contract is shared with the Windows handle.

// joinAs speaks the worker side of the handshake for o.
func joinAs(t *testing.T, o *OwnedWorker, spec Spec) {
	t.Helper()
	conn, err := net.Dial("tcp4", o.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := json.NewEncoder(conn).Encode(workerHello{Identity: spec.Identity, Token: o.Token(), Result: spec.ResultFile, PID: 4242, Created: 1}); err != nil {
		t.Fatal(err)
	}
	var ack [6]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil || string(ack[:]) != "ready\n" {
		t.Fatalf("handshake ack %q, %v", ack, err)
	}
}

// ownedWithFake returns an OwnedWorker bound by handshake to native, or not
// bound at all when native is nil.
func ownedWithFake(t *testing.T, native *fakeNative) (*OwnedWorker, Spec) {
	t.Helper()
	dir := t.TempDir()
	spec := Spec{Identity: Identity{TaskID: "task", AttemptID: "attempt"}, WorkDir: dir,
		PromptFile: filepath.Join(dir, "prompt"), ResultFile: filepath.Join(dir, "result.json")}
	o, err := newOwnedWorker(spec, "worker", func(pid uint32, created uint64, exe string) (nativeWorker, error) {
		if pid != 4242 || created != 1 || exe != "worker" {
			return nil, errors.New("unexpected identity")
		}
		return native, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })
	if native != nil {
		ctx, cancel := context.WithTimeout(context.Background(), testenv.Ceiling(10*time.Second))
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- o.Accept(ctx) }()
		joinAs(t, o, spec)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	return o, spec
}

func assertLateResultRejected(t *testing.T, spec Spec) {
	t.Helper()
	if _, err := ReadResult(spec.ResultFile, spec.Identity); !errors.Is(err, ErrAttemptCancelled) {
		t.Errorf("late read = %v, want ErrAttemptCancelled", err)
	}
	if err := WriteResult(spec.ResultFile, Result{Identity: spec.Identity, Status: StatusSucceeded, Summary: "late", Outputs: []Output{}}); !errors.Is(err, ErrAttemptCancelled) {
		t.Errorf("late write = %v, want ErrAttemptCancelled", err)
	}
	if _, err := os.Stat(spec.ResultFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("late result exists: %v", err)
	}
}

func TestOwnedWorkerStopVerifiedTermination(t *testing.T) {
	native := newFakeNative()
	o, spec := ownedWithFake(t, native)
	if err := o.Stop(context.Background()); err != nil {
		t.Fatalf("Stop = %v", err)
	}
	if native.isRunning() {
		t.Error("Stop returned nil but the native worker still runs")
	}
	if err := o.Stop(context.Background()); err != nil {
		t.Errorf("repeat Stop = %v", err)
	}
	assertLateResultRejected(t, spec)
}

func TestOwnedWorkerStopWithoutProofRejectsLateResult(t *testing.T) {
	t.Run("ownership never established", func(t *testing.T) {
		o, spec := ownedWithFake(t, nil)
		if err := o.Stop(context.Background()); err == nil {
			t.Error("claimed termination without ownership")
		}
		assertLateResultRejected(t, spec)
	})
	t.Run("termination failed", func(t *testing.T) {
		native := newFakeNative()
		native.stopErr = errors.New("termination failed")
		o, spec := ownedWithFake(t, native)
		if err := o.Stop(context.Background()); err == nil {
			t.Error("claimed termination after it failed")
		}
		assertLateResultRejected(t, spec)
	})
	t.Run("handle already released", func(t *testing.T) {
		native := newFakeNative()
		o, spec := ownedWithFake(t, native)
		if err := o.Close(); err != nil {
			t.Fatal(err)
		}
		if err := o.Stop(context.Background()); !errors.Is(err, errWorkerClosed) {
			t.Errorf("Stop after Close = %v, want errWorkerClosed", err)
		}
		if !native.isRunning() {
			t.Error("Stop after Close acted on the worker")
		}
		assertLateResultRejected(t, spec)
	})
}

func TestOwnedWorkerUnsupported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows supplies native handle stopping")
	}
	if _, err := NewOwnedWorker(Spec{}, ""); !errors.Is(err, ErrStopUnsupported) {
		t.Fatalf("unsupported platform = %v", err)
	}
}
