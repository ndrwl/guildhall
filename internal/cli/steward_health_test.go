package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestDirectorStatusHealth: director status --json --copy reports the newest session's supervisor, the last turn
// its runtime's hooks recorded, the messages waiting and the release beside the copy's pin, which is what the
// Steward's status read checks.
func TestDirectorStatusHealth(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n")
	health := func() statusHealth {
		t.Helper()
		code, out, stderr := runCLI(t, "director", "status", "--json", "--copy", dir)
		if code != 0 {
			t.Fatalf("director status: exit %d, %s", code, stderr)
		}
		var feed struct {
			Health *statusHealth `json:"health"`
		}
		if err := json.Unmarshal([]byte(out), &feed); err != nil || feed.Health == nil {
			t.Fatalf("director status printed %s: %v", out, err)
		}
		return *feed.Health
	}
	if h := health(); h.Supervisor != "none" || h.LastTurn != nil || h.Waiting != 0 || h.Release != "" || h.Pin != "" || h.Behind {
		t.Fatalf("no session: %+v", h)
	}
	recordSession(t, filepath.Join(dir, "director.store"), false, store.DirectorRunning)
	if err := os.MkdirAll(sessionDir(dir, 1), 0o700); err != nil {
		t.Fatal(err)
	}
	// No supervisor listens, so the hook exits 1, but the turn is recorded first.
	before := time.Now().UTC().Add(-time.Second)
	if code, _, stderr := runCLI(t, "director", "turn-ended", "--copy", dir); code != 1 || strings.Contains(stderr, "last turn not recorded") {
		t.Fatalf("turn-ended: exit %d, %s", code, stderr)
	}
	if err := release.WritePin(dir, "v0.5.0"); err != nil {
		t.Fatal(err)
	}
	h := health()
	if h.Supervisor != "alive" || h.LastTurn == nil || h.LastTurn.Turn != session.TurnEnded || h.LastTurn.At.Before(before) ||
		h.Release != "test" || h.Pin != "v0.5.0" || h.Behind {
		t.Fatalf("running session: %+v", h)
	}
}

// TestDirectorConditions: a Director is stuck when messages wait and it has taken no turn for the span since the
// later of its last turn and the oldest message, unhealthy when its supervisor is gone or it has stayed recovering
// for the span, and behind when it runs a release older than the pin.
func TestDirectorConditions(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	span := time.Hour
	healthy := steward.DirectorHealth{State: "running", Number: 3, StartedAt: now.Add(-5 * time.Hour), Supervisor: "alive",
		LastTurn: now.Add(-3 * time.Hour), Release: "v0.5.0", Pin: "v0.5.0"}
	for _, c := range []struct {
		name string
		edit func(*steward.DirectorHealth)
		want string
	}{
		{"healthy", func(*steward.DirectorHealth) {}, ""},
		{"waiting briefly", func(h *steward.DirectorHealth) { h.Waiting, h.WaitingSince = 1, now.Add(-10*time.Minute) }, ""},
		{"turned since the message", func(h *steward.DirectorHealth) {
			h.Waiting, h.WaitingSince, h.LastTurn = 2, now.Add(-2*time.Hour), now.Add(-5*time.Minute)
		}, ""},
		{"stuck", func(h *steward.DirectorHealth) { h.Waiting, h.WaitingSince = 2, now.Add(-2*time.Hour) }, "director-stuck"},
		{"stopped and waiting", func(h *steward.DirectorHealth) {
			h.State, h.Supervisor, h.LastTurn, h.Waiting, h.WaitingSince = "stopped", "none", time.Time{}, 1, now.Add(-time.Hour)
		}, "director-stuck"},
		{"supervisor gone", func(h *steward.DirectorHealth) { h.Supervisor = "gone" }, "director-unhealthy"},
		{"supervisor unverified", func(h *steward.DirectorHealth) { h.Supervisor = "unverified" }, ""},
		{"recovering briefly", func(h *steward.DirectorHealth) { h.State, h.StartedAt = "recovering", now.Add(-time.Minute) }, ""},
		{"recovering too long", func(h *steward.DirectorHealth) { h.State = "recovering" }, "director-unhealthy"},
		{"behind", func(h *steward.DirectorHealth) { h.Release, h.Behind = "v0.4.0", true }, "release-behind"},
	} {
		h := healthy
		c.edit(&h)
		var held []string
		for _, dc := range directorConditions(h, span, now) {
			if dc.holds {
				held = append(held, dc.kind)
				if dc.mark == "" || dc.subject == "" || !strings.HasSuffix(dc.text, stewardInformsOnly) {
					t.Errorf("%s: %s holds with mark %q, subject %q, text %q", c.name, dc.kind, dc.mark, dc.subject, dc.text)
				}
			}
		}
		if got := strings.Join(held, ","); got != c.want {
			t.Errorf("%s: conditions %q, want %q", c.name, got, c.want)
		}
	}
}

// healthFeed is a director status feed carrying h, as a copy prints it.
func healthFeed(t *testing.T, h steward.DirectorHealth) []byte {
	t.Helper()
	sh := statusHealth{Supervisor: h.Supervisor, Waiting: h.Waiting, WaitingSince: h.WaitingSince, Release: h.Release, Pin: h.Pin, Behind: h.Behind}
	if !h.LastTurn.IsZero() {
		sh.LastTurn = &session.LastTurn{Turn: session.TurnEnded, At: h.LastTurn}
	}
	b, err := json.Marshal(struct {
		Session statusSession `json:"session"`
		Health  statusHealth  `json:"health"`
	}{statusSession{State: h.State, Number: h.Number, StartedAt: h.StartedAt}, sh})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestScheduledDirectorHealth: the scheduled run reads each enrolled copy's director status and sends one
// director-stuck, director-unhealthy or release-behind report per condition until it clears; release-behind waits
// for report_director_after; a copy whose guildhall reports no health is reported on standard error and sent nothing.
func TestScheduledDirectorHealth(t *testing.T) {
	cs := newCollectSetup(t)
	app, _, stderr := cs.app()
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	app.Now = func() time.Time { return now }
	cfg, err := config.LoadUnadmitted(filepath.Join(cs.steward, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Steward.CollectEvery = "1h"
	w, err := app.stewardCollection(cfg, filepath.Join(cs.steward, stewardStoreFile))
	if err != nil || w == nil {
		t.Fatalf("schedule: %v %v", w, err)
	}
	// The real status read of a copy with no session and nothing waiting finds nothing.
	w.round()
	if es := stewardOutbox(t, cs.steward); len(es) != 0 || strings.Contains(stderr.String(), "director status") {
		t.Fatalf("a healthy copy was reported: %+v; stderr %s", es, stderr)
	}
	var statusRuns int
	for _, argv := range cs.fleet.Ran("guild-ws") {
		if strings.Join(argv, " ") == "guildhall director status --json --copy "+filepath.ToSlash(cs.director) {
			statusRuns++
		}
	}
	if statusRuns != 1 {
		t.Fatalf("director status ran %d times, want once: %q", statusRuns, cs.fleet.Ran("guild-ws"))
	}

	h := steward.DirectorHealth{State: "running", Number: 2, StartedAt: now.Add(-5 * time.Hour), Supervisor: "alive",
		LastTurn: now.Add(-3 * time.Hour), Waiting: 1, WaitingSince: now.Add(-2 * time.Hour), Release: "v0.4.0", Pin: "v0.5.0", Behind: true}
	cs.status = func([]string) ([]byte, bool, error) { return healthFeed(t, h), true, nil }
	kinds := func() map[string]int {
		out := map[string]int{}
		for _, m := range directorExternal(t, cs.director) {
			out[m.Kind]++
		}
		return out
	}
	want := func(step string, stuck, unhealthy, behind int) {
		t.Helper()
		got := kinds()
		if got[store.ReportDirectorStuck] != stuck || got[store.ReportDirectorUnhealthy] != unhealthy || got[store.ReportReleaseBehind] != behind {
			t.Fatalf("%s: the Director's reports %v, want stuck %d, unhealthy %d, behind %d; stderr %s", step, got, stuck, unhealthy, behind, stderr)
		}
	}
	w.round()
	want("stuck found", 1, 0, 0)
	w.round()
	want("stuck found again", 1, 0, 0)
	now = now.Add(2 * time.Hour)
	w.round()
	want("behind for the span", 1, 0, 1)
	// A turn clears the stuck condition; no turn for the span again is a new one.
	h.LastTurn = now
	w.round()
	want("turned", 1, 0, 1)
	now = now.Add(90 * time.Minute)
	w.round()
	want("stuck again", 2, 0, 1)
	// A gone supervisor is reported at once, once.
	h.Supervisor = "gone"
	w.round()
	w.round()
	want("supervisor gone", 2, 1, 1)
	for _, m := range directorExternal(t, cs.director) {
		if m.Sender != store.SenderSteward || !strings.Contains(m.Text, "informs only") || len(m.Subject) > store.MaxSubjectBytes {
			t.Errorf("report %+v", m)
		}
	}

	// A copy whose guildhall predates the health checks is reported on standard error, and sent nothing.
	cs.status = func([]string) ([]byte, bool, error) { return []byte(`{"session":{"state":"running"}}`), true, nil }
	stderr.Reset()
	before := len(directorExternal(t, cs.director))
	w.round()
	if n := len(directorExternal(t, cs.director)); n != before || !strings.Contains(stderr.String(), "predates the Steward's Director health checks") {
		t.Errorf("an old guildhall: %d reports, want %d; stderr %s", n, before, stderr)
	}
	cs.status = func([]string) ([]byte, bool, error) { return nil, true, fmt.Errorf("exit 1: unknown flag") }
	stderr.Reset()
	w.round()
	if !strings.Contains(stderr.String(), "workstation/alpha: director status: exit 1: unknown flag") {
		t.Errorf("a failed status read: stderr %s", stderr)
	}
}
