package cli

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/launch"
)

// boundClaimJSON is a bound pane claim, as a pane launch leaves it once its tab is bound.
const boundClaimJSON = `{"Session":"lab","WorkspaceID":"w9","TabID":"w9:t1","PaneID":"w9:p1"}`

// heldClaim writes o's attempt a bound pane claim and, unless launcher is nil, the launcher its bridge recorded,
// returning the attempt directory.
func heldClaim(t *testing.T, o ownedAttempt, launcher *artisan.Identity) string {
	t.Helper()
	dir := artisan.AttemptKey{Repository: "app", TaskID: "t1", Step: "build", Attempt: 1}.Dir(o.state)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, launch.PaneClaimFile), boundClaimJSON)
	if launcher != nil {
		b, err := json.Marshal(launcher)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, launch.PaneLauncherFile), string(b))
	}
	return dir
}

// goneLauncher is an identity no live process has: this process's pid with another start token.
func goneLauncher(t *testing.T) *artisan.Identity {
	t.Helper()
	self, err := artisan.Self()
	if err != nil {
		t.Fatal(err)
	}
	self.Token += "-gone"
	return &self
}

type paneClaimResume struct {
	Owner          string         `json:"owner"`
	PaneClaim      string         `json:"pane_claim"`
	Classification classification `json:"classification"`
}

// TestArtisanResumeAbandonsAPaneClaimWhoseLauncherIsGone: the d216 shape, a bound claim with no owner whose
// recorded launcher is gone, is named by status and set aside by resume, so the next start claims afresh.
func TestArtisanResumeAbandonsAPaneClaimWhoseLauncherIsGone(t *testing.T) {
	o := newOwnedAttempt(t, "")
	dir := heldClaim(t, o, goneLauncher(t))
	code, stdout, stderr := guildhall(t, "", o.args("status")...)
	if c := decodeJSON[classification](t, stdout); code != 0 || c.State != "absent" || !strings.Contains(c.Reason, "pane claim held, no owner") || !strings.Contains(c.Reason, "proven gone") {
		t.Fatalf("status: exit %d, %+v; stderr %q", code, c, stderr)
	}
	code, stdout, stderr = guildhall(t, "", o.args("resume")...)
	r := decodeJSON[paneClaimResume](t, stdout)
	if code != 0 || r.Owner != launch.OwnerNone || r.PaneClaim != launch.PaneClaimAbandoned || r.Classification.State != "absent" {
		t.Fatalf("resume: exit %d, %+v; stderr %q", code, r, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, launch.PaneClaimFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("claim still held: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "pane-launch.abandoned-1.json")); err != nil || string(b) != boundClaimJSON {
		t.Fatalf("abandoned claim = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pane-launcher.abandoned-1.json")); err != nil {
		t.Fatal(err)
	}
	// The next start's exclusive claim succeeds.
	f, err := os.OpenFile(filepath.Join(dir, launch.PaneClaimFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("next claim refused: %v", err)
	}
	_ = f.Close()
}

// TestArtisanResumeRefusesAnUnprovenPaneClaim: a claim whose launcher was never recorded, or is live, is refused
// without the flag, naming it, and with the flag but no terminal that confirms its pane gone; nothing is renamed.
func TestArtisanResumeRefusesAnUnprovenPaneClaim(t *testing.T) {
	self, err := artisan.Self()
	if err != nil {
		t.Fatal(err)
	}
	for name, launcher := range map[string]*artisan.Identity{"unrecorded": nil, "live": &self} {
		t.Run(name, func(t *testing.T) {
			o := newOwnedAttempt(t, "")
			dir := heldClaim(t, o, launcher)
			for _, extra := range [][]string{nil, {launch.AbandonPaneClaimFlag}} {
				code, stdout, stderr := guildhall(t, "", o.args("resume", extra...)...)
				r := decodeJSON[paneClaimResume](t, stdout)
				if code != 1 || r.PaneClaim != "" || (extra == nil && !strings.Contains(stderr, launch.AbandonPaneClaimFlag)) ||
					(extra != nil && !strings.Contains(stderr, "cannot be confirmed gone")) {
					t.Fatalf("resume %q: exit %d, %+v; stderr %q", extra, code, r, stderr)
				}
				if b, err := os.ReadFile(filepath.Join(dir, launch.PaneClaimFile)); err != nil || string(b) != boundClaimJSON {
					t.Fatalf("claim changed to %q, %v", b, err)
				}
				if _, err := os.Stat(filepath.Join(dir, "pane-launch.abandoned-1.json")); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("claim set aside: %v", err)
				}
			}
		})
	}
}

// TestArtisanResumeAbandonNeverRemovesAnOwner: with an ownership record, the flag is refused and the claim kept.
func TestArtisanResumeAbandonNeverRemovesAnOwner(t *testing.T) {
	o := newOwnedAttempt(t, "guildhall-fake: result=never\nguildhall-fake: lifetime=60s\n")
	_, exited, _ := launcherProcess(t, o)
	dir := heldClaim(t, o, goneLauncher(t))
	code, stdout, stderr := guildhall(t, "", o.args("resume", launch.AbandonPaneClaimFlag)...)
	if r := decodeJSON[paneClaimResume](t, stdout); code != 1 || r.Owner != launch.OwnerLive || r.PaneClaim != "" {
		t.Errorf("resume --abandon-pane-claim over an owner: exit %d, %+v; stderr %q", code, r, stderr)
	}
	if b, err := os.ReadFile(filepath.Join(dir, launch.PaneClaimFile)); err != nil || string(b) != boundClaimJSON {
		t.Errorf("claim changed to %q, %v", b, err)
	}
	if code, stdout, stderr := guildhall(t, "", o.args("stop")...); code != 0 {
		t.Fatalf("stop: exit %d; %q; %q", code, stdout, stderr)
	}
	<-exited
}

// TestArtisanStartLockWaitReachesTheAttempt: `artisan start --lock-wait` is the attempt's store lock wait.
func TestArtisanStartLockWaitReachesTheAttempt(t *testing.T) {
	code, _, stderr := guildhall(t, "", newOwnedAttempt(t, "").args("start", "--lock-wait", "-1s")...)
	if code != 2 || !strings.Contains(stderr, "--lock-wait must not be negative") {
		t.Fatalf("negative --lock-wait: exit %d; stderr %q", code, stderr)
	}
	o := newOwnedAttempt(t, "")
	f := attemptFlags{store: o.store, state: o.state, repo: "app", task: "t1", step: "build", attempt: "1", lockWait: 45e9}
	at, err := f.parse(nil)
	if err != nil || at.LockWait != 45e9 {
		t.Fatalf("parse = %+v, %v", at, err)
	}
}
