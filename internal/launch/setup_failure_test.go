package launch_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/store"
)

func TestRunPreflightFailureDoesNotTakeOwnership(t *testing.T) {
	a := newAttempt(t)
	bad := a
	bad.StateDir = filepath.Join(a.StateDir, strings.Repeat("x", artisan.MaxEndpointPath))
	cmd, inv := worker(t, a, artisantest.Worker{Lifetime: 100 * time.Millisecond})
	rep, err := launch.Run(context.Background(), launch.Config{Attempt: bad, Command: cmd})
	if !errors.Is(err, artisan.ErrEndpointPath) || rep.AttemptOutcome != "" {
		t.Fatalf("Run = %+v, %v; want endpoint preflight error", rep, err)
	}
	withStore(t, a, func(s *store.Store) {
		if o, err := s.Ownership(key.Repository, key.TaskID, key.Step, key.Attempt); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("ownership = %+v, %v; want no record", o, err)
		}
	})
	if entries, err := artisantest.Inventory(inv); err != nil || len(entries) != 0 {
		t.Fatalf("worker inventory = %v, %v; want no worker", entries, err)
	}
	done, cancel := start(a, cmd)
	defer cancel()
	if r := wait(t, done); r.err != nil || r.r.AttemptOutcome != store.AttemptFailed {
		t.Fatalf("corrected retry = %+v, %v", r.r, r.err)
	}
}

func TestRunEndpointSetupFailureFinishesAttempt(t *testing.T) {
	a := newAttempt(t)
	path, err := artisan.EndpointPath(a.StateDir, a.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd, inv := worker(t, a, artisantest.Worker{})
	rep, err := launch.Run(context.Background(), launch.Config{Attempt: a, Command: cmd})
	if !errors.Is(err, artisan.ErrEndpointUnknown) || rep.AttemptOutcome != store.AttemptFailed || storedOutcome(t, a) != store.AttemptFailed {
		t.Fatalf("Run = %+v, %v; want endpoint error and failed attempt", rep, err)
	}
	if o := ownership(t, a); o.State != store.OwnerReleased {
		t.Fatalf("ownership = %+v; want released", o)
	}
	if entries, err := artisantest.Inventory(inv); err != nil || len(entries) != 0 {
		t.Fatalf("worker inventory = %v, %v; want no worker", entries, err)
	}
}
