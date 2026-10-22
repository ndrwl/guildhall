package launch_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/store"
)

func TestRunDecidedAttemptPreservesOwnership(t *testing.T) {
	// One exited owner serves every case: it is gone, and each case records it in its own store.
	id, containment := exitedOwner(t)
	for _, state := range []artisan.OutcomeState{artisan.Finished, artisan.Cancelled, artisan.Failed} {
		for _, blocked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/blocked-endpoint=%t", state, blocked), func(t *testing.T) {
				a := newAttempt(t)
				endpoint, err := artisan.EndpointPath(a.StateDir, a.Key)
				if err != nil {
					t.Fatal(err)
				}
				recordOwner(t, a, id, containment, endpoint)
				before := ownership(t, a)
				if lv, err := launch.ProbeOwner(before); err != nil || lv != store.ProbeGone {
					t.Fatalf("prior owner = %s, %v; want verified empty unit", lv, err)
				}
				body, err := json.Marshal(artisan.Outcome{Schema: artisan.OutcomeSchema, State: state, Detail: "decided before owner exited"})
				if err != nil {
					t.Fatal(err)
				}
				outcomePath := filepath.Join(a.Dir(), artisan.OutcomeFile)
				if err := os.WriteFile(outcomePath, body, 0o600); err != nil {
					t.Fatal(err)
				}
				if blocked {
					if err := os.MkdirAll(endpoint, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				cmd, inv := worker(t, a, artisantest.Worker{})
				rep, err := launch.Run(context.Background(), launch.Config{Attempt: a, Command: cmd})
				if !errors.Is(err, artisan.ErrDecided) || rep.AttemptOutcome != "" {
					t.Fatalf("Run = %+v, %v; want ErrDecided without finalization", rep, err)
				}
				if after := ownership(t, a); !reflect.DeepEqual(before, after) {
					t.Fatalf("ownership changed: before %+v, after %+v", before, after)
				}
				if got := storedOutcome(t, a); got != "" {
					t.Fatalf("store attempt finalized as %q", got)
				}
				if got, err := os.ReadFile(outcomePath); err != nil || string(got) != string(body) {
					t.Fatalf("persisted outcome changed: %q, %v", got, err)
				}
				if entries, err := artisantest.Inventory(inv); err != nil || len(entries) != 0 {
					t.Fatalf("worker inventory = %v, %v; want no worker", entries, err)
				}
			})
		}
	}
}
