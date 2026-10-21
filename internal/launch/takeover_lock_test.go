package launch_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/launch"
)

func TestRunTakeoverWaitsForOutcomePublication(t *testing.T) {
	for _, publish := range []bool{false, true} {
		name := "timeout"
		if publish {
			name = "publication"
		}
		t.Run(name, func(t *testing.T) {
			a := newAttempt(t)
			id, containment := exitedOwner(t)
			recordOwner(t, a, id, containment, "")
			before := ownership(t, a)
			ctx, cancel := context.WithTimeout(context.Background(), deadline)
			defer cancel()
			release, err := artisan.LockAttempt(ctx, a.Dir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if release != nil {
					release()
				}
			}()
			cmd, inv := worker(t, a, artisantest.Worker{Lifetime: 100 * time.Millisecond})
			runCtx := ctx
			if !publish {
				var stop context.CancelFunc
				runCtx, stop = context.WithTimeout(ctx, 100*time.Millisecond)
				defer stop()
			}
			done := make(chan ran, 1)
			go func() {
				rep, err := launch.Run(runCtx, launch.Config{Attempt: a, Command: cmd})
				done <- ran{rep, err}
			}()
			if publish {
				select {
				case r := <-done:
					t.Fatalf("Run returned while publication lock held: %+v, %v", r.r, r.err)
				case <-time.After(200 * time.Millisecond):
				}
				if after := ownership(t, a); !reflect.DeepEqual(before, after) {
					t.Fatalf("ownership changed under publication lock: %+v", after)
				}
				if err := os.WriteFile(filepath.Join(a.Dir(), artisan.OutcomeFile), []byte(`{"schema":"guildhall.attempt-outcome/1","state":"finished","detail":"published before takeover"}`), 0600); err != nil {
					t.Fatal(err)
				}
				release()
				release = nil
			}
			r := wait(t, done)
			want := context.DeadlineExceeded
			if publish {
				want = artisan.ErrDecided
			}
			if !errors.Is(r.err, want) {
				t.Fatalf("Run = %+v, %v; want %v", r.r, r.err, want)
			}
			if after := ownership(t, a); !reflect.DeepEqual(before, after) {
				t.Fatalf("ownership changed: before %+v, after %+v", before, after)
			}
			if got := storedOutcome(t, a); got != "" {
				t.Fatalf("attempt finalized as %q", got)
			}
			if entries, err := artisantest.Inventory(inv); err != nil || len(entries) != 0 {
				t.Fatalf("worker inventory = %v, %v", entries, err)
			}
		})
	}
}
