package cli

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// Exercise shutdown in a real launcher process with an unfinished transport
// exchange: stop must get its reply, new callers must be refused, and the
// existing silent peer must be drained by the transport deadline.
func TestArtisanStopDrainsSilentPeer(t *testing.T) {
	o := newOwnedAttempt(t, "guildhall-fake: delay=5m\n")
	run, exited, runErr := launcherProcess(t, o)
	owner := o.ownership(t)
	ctx, cancel := context.WithTimeout(context.Background(), testenv.Ceiling(30*time.Second))
	defer cancel()
	key := artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1}
	wrong := key
	wrong.TaskID = "not-this-task"
	reply, err := artisan.Call(ctx, owner.Endpoint, wrong, artisan.Identity(owner.LauncherIdentity), artisan.OpStop)
	t.Logf("mismatched stop: reply %+v; error %v", reply, err)
	if !errors.Is(err, artisan.ErrEndpointMismatch) {
		t.Fatalf("mismatched stop: got %v, want ErrEndpointMismatch", err)
	}
	if code, out, stderr := guildhall(t, "", o.args("status")...); code != 0 || decodeJSON[classification](t, out).State != "running" {
		t.Fatalf("mismatched stop changed the running attempt: exit %d; %s; %s", code, out, stderr)
	}

	peer, err := net.DialTimeout("unix", owner.Endpoint, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	if _, err := peer.Write([]byte(`{"schema":`)); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := guildhall(t, "", o.args("stop")...)
	t.Logf("artisan stop with unfinished peer: exit %d; stdout %s; stderr %s", code, out, stderr)
	if code != 0 || !strings.Contains(out, `"endpoint":"stopped"`) || !strings.Contains(out, `"state":"cancelled"`) {
		t.Fatalf("stop did not deliver its complete reply: exit %d; %s; %s", code, out, stderr)
	}
	for {
		_, err := os.Stat(owner.Endpoint)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("socket path was not removed during shutdown")
		case <-time.After(10 * time.Millisecond): // timing:allow endpoint-readiness polling bounded by ctx.
		}
	}
	select {
	case <-exited:
		t.Fatal("launcher exited before the silent exchange reached its deadline")
	default:
	}
	if c, err := net.DialTimeout("unix", owner.Endpoint, time.Second); err == nil {
		_ = c.Close()
		t.Fatal("endpoint accepted a new connection while shutting down")
	}
	t.Log("socket removed and new connections refused while launcher drains the accepted peer")
	_ = peer.SetReadDeadline(time.Now().Add(testenv.Ceiling(10 * time.Second)))
	line, err := bufio.NewReader(peer).ReadString('\n')
	if err != nil {
		t.Fatalf("silent peer did not receive its deadline refusal: %v", err)
	}
	type refusalReply struct {
		Schema   string           `json:"schema"`
		Attempt  string           `json:"attempt"`
		Launcher artisan.Identity `json:"launcher"`
		Error    string           `json:"error"`
	}
	refusal := decodeJSON[refusalReply](t, line)
	t.Logf("silent peer's transport-deadline reply: %s", line)
	if refusal.Schema != "guildhall.attempt-control/1" || !strings.Contains(refusal.Error, "malformed request") {
		t.Fatalf("unexpected silent-peer refusal: %s", line)
	}
	select {
	case <-exited:
	case <-ctx.Done():
		t.Fatalf("launcher did not exit after draining: %s", runErr.String())
	}
	if run.ProcessState.ExitCode() != 5 || o.ownership(t).State != store.OwnerReleased {
		t.Fatalf("launcher exit %d or ownership not released", run.ProcessState.ExitCode())
	}
	t.Logf("launcher exited %d with released ownership after draining", run.ProcessState.ExitCode())
}
