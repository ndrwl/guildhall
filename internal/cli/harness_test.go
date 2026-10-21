package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
)

type harnessProcess struct {
	Role     string `json:"role"`
	PID      int    `json:"pid"`
	Token    string `json:"token"`
	Liveness string `json:"liveness"`
}

func TestArtisanHarnessNeedsLab(t *testing.T) {
	o := newOwnedAttempt(t, "x")
	for _, cmd := range []string{"inventory", "kill-owned", "probe"} {
		app, _, stderr := newArtisanApp(t)
		if code := app.Run(append([]string{"artisan", "harness", cmd}, o.args("status")[2:]...)); code != 2 {
			t.Errorf("harness %s without --lab: exit %d, want 2; stderr %q", cmd, code, stderr)
		}
	}
	for _, bad := range [][]string{
		{"artisan", "harness", "--lab"},
		{"artisan", "harness", "sweep", "--lab"},
		{"artisan", "harness", "inventory", "--lab"},
		{"artisan", "harness", "probe", "--lab", "extra"},
	} {
		app, _, stderr := newArtisanApp(t)
		if code := app.Run(bad); code != 2 {
			t.Errorf("%q: exit %d, want 2; stderr %q", bad, code, stderr)
		}
	}
}

func TestArtisanHarnessProbe(t *testing.T) {
	self, err := artisan.Self()
	if err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal([]artisan.Identity{self, {PID: self.PID, Token: self.Token + "x"}})
	code, stdout, stderr := guildhall(t, string(in), "artisan", "harness", "probe", "--lab")
	got := decodeJSON[[]harnessProcess](t, stdout)
	if code != 0 || len(got) != 2 || got[0].Liveness != "alive" || got[1].Liveness != "gone" || got[0].PID != self.PID {
		t.Errorf("probe: exit %d, %+v; stderr %q", code, got, stderr)
	}
	if code, _, _ := guildhall(t, "not json", "artisan", "harness", "probe", "--lab"); code != 1 {
		t.Errorf("probe of malformed input: exit %d, want 1", code)
	}
}

// The harness lists what an attempt's records name, kills only what still
// verifies, by identity, and leaves nothing it did not record.
func TestArtisanHarnessInventoryAndKillOwned(t *testing.T) {
	inv := t.TempDir()
	o := newOwnedAttempt(t, "guildhall-fake: result=never\nguildhall-fake: lifetime=60s\nguildhall-fake: grandchildren=1\nguildhall-fake: inventory="+inv+"\n")
	run, exited, _ := launcherProcess(t, o)
	tree := inventoryOf(t, inv, 2)
	list := func() []harnessProcess {
		t.Helper()
		code, stdout, stderr := guildhall(t, "", append([]string{"artisan", "harness", "inventory", "--lab", "--inventory", inv}, o.args("status")[2:]...)...)
		if code != 0 {
			t.Fatalf("inventory: exit %d; stderr %q", code, stderr)
		}
		return decodeJSON[[]harnessProcess](t, stdout)
	}
	roles := map[string]int{}
	for _, p := range list() {
		if p.Liveness != "alive" {
			t.Errorf("%+v is not alive while the launcher runs", p)
		}
		roles[p.Role]++
	}
	if roles["launcher"] != 1 || roles["worker"] < 1 || roles["grandchild"] != 1 {
		t.Errorf("inventory roles %v; want the launcher, the worker and one grandchild", roles)
	}
	code, stdout, stderr := guildhall(t, "", append([]string{"artisan", "harness", "kill-owned", "--lab", "--inventory", inv}, o.args("status")[2:]...)...)
	if killed := decodeJSON[[]harnessProcess](t, stdout); code != 0 || len(killed) < 3 {
		t.Errorf("kill-owned: exit %d, %+v; stderr %q", code, killed, stderr)
	}
	<-exited
	for _, id := range append(tree, artisan.Identity{PID: run.Process.Pid}) {
		if id.Token == "" {
			continue
		}
		if lv, _ := artisan.Probe(id); lv != artisan.Gone {
			t.Errorf("%v is %v after kill-owned", id, lv)
		}
	}
	for _, p := range list() {
		if p.Liveness != "gone" {
			t.Errorf("%+v survived kill-owned", p)
		}
	}
	if !strings.Contains(stdout, `"role":"launcher"`) {
		t.Errorf("kill-owned did not name the launcher: %s", stdout)
	}
}
