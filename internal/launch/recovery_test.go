package launch_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/store"
)

func exitedOwner(t *testing.T) (artisan.Identity, store.Containment) {
	t.Helper()
	u, err := artisan.NewUnit(fmt.Sprintf("recovery-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close() })
	id, err := u.Start(artisantest.Command(t.TempDir(), artisantest.Worker{Lifetime: 100 * time.Millisecond}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-u.Exited():
	case <-time.After(deadline):
		t.Fatal("worker did not exit")
	}
	c := u.Containment()
	return id, store.Containment{Kind: c.Kind, Name: c.Name, Reason: c.Reason, Worker: store.ProcessIdentity(id)}
}

func recordOwner(t *testing.T, a launch.Attempt, id artisan.Identity, c store.Containment, endpoint string) {
	t.Helper()
	o := store.Ownership{Repository: key.Repository, TaskID: key.TaskID, Step: key.Step, Attempt: key.Attempt, LauncherIdentity: store.ProcessIdentity(id)}
	withStore(t, a, func(s *store.Store) {
		if _, err := s.TakeOwnership("prior", o, launch.ProbeOwner); err != nil {
			t.Fatal(err)
		}
		if c != (store.Containment{}) || endpoint != "" {
			o.Containment, o.Endpoint = c, endpoint
			o.State = store.OwnerRunning
			if _, err := s.UpdateOwnership("record", o); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestRunRecoveryRequiresRecordedEmptyContainment(t *testing.T) {
	for _, recorded := range []bool{false, true} {
		t.Run(fmt.Sprint(recorded), func(t *testing.T) {
			a := newAttempt(t)
			id, c := exitedOwner(t)
			if !recorded {
				c = store.Containment{}
			}
			recordOwner(t, a, id, c, "")
			cmd, inv := worker(t, a, artisantest.Worker{Lifetime: 100 * time.Millisecond})
			done, cancel := start(a, cmd)
			defer cancel()
			r := wait(t, done)
			if recorded {
				if r.err != nil || r.r.AttemptOutcome != store.AttemptFailed {
					t.Fatalf("verified takeover: %+v, %v", r.r, r.err)
				}
			} else {
				if !errors.Is(r.err, store.ErrOwnerUnknown) {
					t.Fatalf("unproven takeover: %v", r.err)
				}
				if entries, _ := artisantest.Inventory(inv); len(entries) != 0 {
					t.Fatalf("started worker: %v", entries)
				}
				if o := ownership(t, a); o.LauncherIdentity != store.ProcessIdentity(id) {
					t.Fatalf("replaced owner: %+v", o)
				}
			}
		})
	}
}

func TestStopTerminalDecisionRequiresTerminationProof(t *testing.T) {
	for _, decision := range []string{"cancelled", "finished", "failed"} {
		for _, proof := range []string{"live", "missing", "survivors", "empty"} {
			t.Run(decision+"/"+proof, func(t *testing.T) {
				a := newAttempt(t)
				id, c := exitedOwner(t)
				if proof == "live" {
					var err error
					id, err = artisan.Self()
					if err != nil {
						t.Fatal(err)
					}
				}
				if proof == "missing" {
					c = store.Containment{}
				}
				if proof == "survivors" {
					u, err := artisan.NewUnit(fmt.Sprintf("survivor-%d", time.Now().UnixNano()))
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = u.Close() })
					w, err := u.Start(artisantest.Command(t.TempDir(), artisantest.Worker{Lifetime: deadline}))
					if err != nil {
						t.Fatal(err)
					}
					unit := u.Containment()
					c = store.Containment{Kind: unit.Kind, Name: unit.Name, Reason: unit.Reason, Worker: store.ProcessIdentity(w)}
				}
				recordOwner(t, a, id, c, "")
				switch decision {
				case "cancelled":
					if err := a.Files().Mark(); err != nil {
						t.Fatal(err)
					}
				case "finished":
					if err := os.WriteFile(a.Files().ResultFile, []byte(resultBody(t, a.Identity(), adapter.StatusSucceeded)), 0600); err != nil {
						t.Fatal(err)
					}
				case "failed":
					if err := os.WriteFile(a.Dir()+"/"+artisan.OutcomeFile, []byte(`{"schema":"guildhall.attempt-outcome/1","state":"failed","detail":"worker exited","rung":0}`), 0600); err != nil {
						t.Fatal(err)
					}
				}
				rep, err := launch.Stop(context.Background(), a)
				if string(rep.Classification.State) != decision {
					t.Fatalf("classification: %+v", rep)
				}
				if proof == "empty" {
					if err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, launch.ErrNotStopped) {
					t.Fatalf("unproven stop: %+v, %v", rep, err)
				}
			})
		}
	}
}

type failedStopHandler struct{}

func (failedStopHandler) Status() (artisan.EndpointState, error) {
	return artisan.EndpointState{Phase: "ended"}, nil
}
func (failedStopHandler) Cancel() (artisan.EndpointState, error) { return artisan.EndpointState{}, nil }
func (failedStopHandler) Stop() (artisan.EndpointState, error) {
	return artisan.EndpointState{}, artisan.ErrNotEmpty
}

func TestStopPreservesEndpointFailure(t *testing.T) {
	a := newAttempt(t)
	self, err := artisan.Self()
	if err != nil {
		t.Fatal(err)
	}
	path, err := artisan.EndpointPath(a.StateDir, key)
	if err != nil {
		t.Fatal(err)
	}
	recordOwner(t, a, self, store.Containment{}, path)
	ep, err := artisan.Listen(path, key, self, func() (artisan.Identity, error) { return self, nil }, failedStopHandler{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ep.Close() }()
	if err := a.Files().Mark(); err != nil {
		t.Fatal(err)
	}
	rep, err := launch.Stop(context.Background(), a)
	if !errors.Is(err, artisan.ErrEndpointRequest) || !errors.Is(err, launch.ErrNotStopped) || !strings.Contains(err.Error(), artisan.ErrNotEmpty.Error()) {
		t.Fatalf("Stop: %+v, %v", rep, err)
	}
}
