package cli

import (
	"os"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/store"
)

// Even an immediately expiring result budget must own and start the attempt,
// then stop it, rather than failing to acquire ownership.
func TestArtisanResultTimeoutDoesNotBoundOwnership(t *testing.T) {
	o := newOwnedAttempt(t, "guildhall-fake: delay=5m\n")
	code, out, stderr := guildhall(t, "", o.args("start", "--timeout", "1ns")...) // timing:allow immediate result expiry is the input, not a startup deadline.
	if code != exitStopped {
		t.Fatalf("start: exit %d, output %s, stderr %s; want stopped", code, out, stderr)
	}
	r := decodeJSON[runReport](t, lastLine(out))
	own := o.ownership(t)
	if own.Containment.Worker.PID == 0 {
		t.Fatalf("result timeout did not start and record the worker: %+v", own)
	}
	if r.AttemptOutcome != "stopped" || own.State != store.OwnerReleased {
		t.Fatalf("report %+v; want stopped and released", r)
	}
}

// A pre-existing marker deterministically selects the same no-worker path as
// an endpoint stop arriving between ownership and worker startup.
func TestArtisanStopProvesAPreStartCancellation(t *testing.T) {
	o := newOwnedAttempt(t, "guildhall-fake: delay=5m\n")
	at := launch.Attempt{Store: o.store, StateDir: o.state, Key: artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1}}
	if err := os.MkdirAll(at.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := at.Files().Mark(); err != nil {
		t.Fatal(err)
	}
	if code, out, stderr := guildhall(t, "", o.args("start")...); code != exitStopped {
		t.Fatalf("start: exit %d, output %s, stderr %s", code, out, stderr)
	}
	if code, out, stderr := guildhall(t, "", o.args("stop")...); code != 0 {
		t.Fatalf("stop after pre-start cancellation: exit %d, output %s, stderr %s", code, out, stderr)
	}
}
