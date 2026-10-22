package adapter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/testenv"
)

// nativeWorker is the operating-system boundary behind OwnedWorker.Stop.
// testNativeWorker is its contract; it runs against the real Windows handle on
// a live helper process (native_windows_test.go) and against the fakeNative
// double the OwnedWorker tests use. Hosts without native stopping have no real
// implementation; TestOwnedWorkerUnsupported covers their refusal.

type nativeUnderTest struct {
	w nativeWorker
	// exited reports whether the process has exited.
	exited func() bool
	// finish makes the process exit on its own.
	finish func()
}

func testNativeWorker(t *testing.T, open func(t *testing.T) nativeUnderTest) {
	ctx := context.Background()
	waitExit := func(t *testing.T, n nativeUnderTest) {
		t.Helper()
		deadline := time.Now().Add(testenv.Ceiling(5 * time.Second))
		for !n.exited() {
			if time.Now().After(deadline) {
				t.Fatalf("process did not exit within %v", testenv.Ceiling(5*time.Second))
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Run("stop terminates the process and proves it", func(t *testing.T) {
		n := open(t)
		if err := n.w.stop(ctx); err != nil {
			t.Fatalf("stop = %v", err)
		}
		// stop proves exit through the handle; an outside observer such as
		// a Wait call may see it a moment later.
		waitExit(t, n)
	})
	t.Run("repeated and concurrent stops all succeed", func(t *testing.T) {
		n := open(t)
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- n.w.stop(ctx)
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Errorf("stop = %v", err)
			}
		}
		if err := n.w.stop(ctx); err != nil {
			t.Errorf("repeat stop = %v", err)
		}
		waitExit(t, n)
	})
	t.Run("stop after the process exited on its own succeeds", func(t *testing.T) {
		n := open(t)
		n.finish()
		waitExit(t, n)
		if err := n.w.stop(ctx); err != nil {
			t.Errorf("stop = %v", err)
		}
	})
	t.Run("an ended context refuses without terminating", func(t *testing.T) {
		n := open(t)
		done, cancel := context.WithCancel(ctx)
		cancel()
		if err := n.w.stop(done); !errors.Is(err, context.Canceled) {
			t.Errorf("stop = %v, want context.Canceled", err)
		}
		if n.exited() {
			t.Error("a refused stop terminated the process")
		}
	})
	t.Run("close is idempotent and stop after close acts on nothing", func(t *testing.T) {
		n := open(t)
		if err := n.w.close(); err != nil {
			t.Fatalf("close = %v", err)
		}
		if err := n.w.close(); err != nil {
			t.Errorf("second close = %v", err)
		}
		if err := n.w.stop(ctx); !errors.Is(err, errWorkerClosed) {
			t.Errorf("stop after close = %v, want errWorkerClosed", err)
		}
		if n.exited() {
			t.Error("stop after close terminated the process")
		}
	})
}

// fakeNative is the nativeWorker double: a simulated process that runs until
// stopped or finished, with an optional termination failure.
type fakeNative struct {
	mu      sync.Mutex
	running bool
	closed  bool
	stopErr error
	stops   int
}

func newFakeNative() *fakeNative { return &fakeNative{running: true} }

func (f *fakeNative) stop(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errWorkerClosed
	}
	if !f.running {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.stopErr != nil {
		return f.stopErr
	}
	f.stops++
	f.running = false
	return nil
}

func (f *fakeNative) close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeNative) isRunning() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

func TestFakeNativeWorkerContract(t *testing.T) {
	testNativeWorker(t, func(t *testing.T) nativeUnderTest {
		f := newFakeNative()
		return nativeUnderTest{
			w:      f,
			exited: func() bool { return !f.isRunning() },
			finish: func() {
				f.mu.Lock()
				f.running = false
				f.mu.Unlock()
			},
		}
	})
}
