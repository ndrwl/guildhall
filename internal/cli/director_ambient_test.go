package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/store"
)

// herdrEnv is the environment of a Herdr pane, w3:pG in tab w3:t1 of workspace w3 in session lab, with overrides;
// an override of "" unsets the variable.
func herdrEnv(overrides map[string]string) func(string) string {
	env := map[string]string{"HERDR_SESSION": "lab", "HERDR_WORKSPACE_ID": "w3", "HERDR_TAB_ID": "w3:t1", "HERDR_PANE_ID": "w3:pG"}
	for k, v := range overrides {
		env[k] = v
	}
	return func(k string) string { return env[k] }
}

// herdrPane is the pane herdrEnv names, adopted.
var herdrPane = store.DirectorPane{Session: "lab", Workspace: "w3", Tab: "w3:t1", Pane: "w3:pG", Adopted: true}

func TestAmbientPane(t *testing.T) {
	if p, reason := ambientPane(herdrEnv(nil)); p != herdrPane || reason != "" {
		t.Errorf("complete HERDR_*: %+v, %q; want %+v", p, reason, herdrPane)
	}
	for _, c := range []struct {
		name   string
		getenv func(string) string
		reason string
	}{
		{"nil getenv", nil, "HERDR_SESSION not set"},
		{"no session", herdrEnv(map[string]string{"HERDR_SESSION": ""}), "HERDR_SESSION not set"},
		{"default session", herdrEnv(map[string]string{"HERDR_SESSION": "default"}), `HERDR_SESSION is "default"`},
		{"no workspace", herdrEnv(map[string]string{"HERDR_WORKSPACE_ID": ""}), "HERDR_WORKSPACE_ID not set"},
		{"no tab", herdrEnv(map[string]string{"HERDR_TAB_ID": ""}), "HERDR_TAB_ID not set"},
		{"no pane", herdrEnv(map[string]string{"HERDR_PANE_ID": ""}), "HERDR_PANE_ID not set"},
		{"bad pane", herdrEnv(map[string]string{"HERDR_PANE_ID": "-w3:pG"}), `HERDR_PANE_ID "-w3:pG" is not a Herdr id`},
		{"bad session", herdrEnv(map[string]string{"HERDR_SESSION": "la b"}), `HERDR_SESSION "la b" is not a Herdr session name`},
	} {
		if p, reason := ambientPane(c.getenv); p != (store.DirectorPane{}) || reason != c.reason {
			t.Errorf("%s: %+v, %q; want no pane, %q", c.name, p, reason, c.reason)
		}
	}
}

// runDirectorEnv is runDirectorCmd with the environment getenv.
func runDirectorEnv(t *testing.T, getenv func(string) string, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := supervisorApp(nil, time.Hour)
	app.Stdin = strings.NewReader("")
	app.Getenv = getenv
	code := app.Run(append([]string{"director"}, args...))
	return code, stdout.String(), stderr.String()
}

// TestDirectorStartBindsTheHerdrPaneItRunsIn is the d51 regression: a plain director start inside a Herdr pane
// records that pane, adopted, instead of no pane.
func TestDirectorStartBindsTheHerdrPaneItRunsIn(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n", "/exit")
	code, out, stderr := runDirectorEnv(t, herdrEnv(nil), "start", "--copy", dir)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, stderr)
	}
	if got := sessions(t, dir); len(got) != 1 || got[0].Pane != herdrPane {
		t.Errorf("sessions = %+v; want pane %+v", got, herdrPane)
	}
	if want := "pane: w3:pG (tab w3:t1, workspace w3, from HERDR_*)\n"; !strings.Contains(out, want) {
		t.Errorf("stdout lacks %q:\n%s", want, out)
	}
	if strings.Contains(stderr, "no Herdr pane") {
		t.Errorf("warned of no pane: %s", stderr)
	}
}

func TestDirectorStartFlagsWinOverHerdrEnv(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n", "/exit")
	code, out, stderr := runDirectorEnv(t, herdrEnv(nil), "start", "--copy", dir,
		"--herdr-session", "lab", "--herdr-workspace", "w2", "--herdr-tab", "w2:t3", "--herdr-pane", "w2:p3")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, stderr)
	}
	if got := sessions(t, dir); len(got) != 1 || got[0].Pane != (store.DirectorPane{Session: "lab", Workspace: "w2", Tab: "w2:t3", Pane: "w2:p3"}) {
		t.Errorf("sessions = %+v; want the flags' pane, not adopted", got)
	}
	if strings.Contains(out, "from HERDR_*") || strings.Contains(stderr, "no Herdr pane") {
		t.Errorf("flag-bound start mentioned the environment:\n%s\n%s", out, stderr)
	}
}

func TestDirectorStartWarnsWithoutAPane(t *testing.T) {
	for name, c := range map[string]struct {
		getenv func(string) string
		reason string
	}{
		"outside herdr":   {nil, "HERDR_SESSION not set"},
		"no session":      {herdrEnv(map[string]string{"HERDR_SESSION": ""}), "HERDR_SESSION not set"},
		"default session": {herdrEnv(map[string]string{"HERDR_SESSION": "default"}), `HERDR_SESSION is "default"`},
		"no tab":          {herdrEnv(map[string]string{"HERDR_TAB_ID": ""}), "HERDR_TAB_ID not set"},
		"bad id":          {herdrEnv(map[string]string{"HERDR_PANE_ID": "--all"}), `HERDR_PANE_ID "--all" is not a Herdr id`},
	} {
		t.Run(name, func(t *testing.T) {
			dir := directorCopy(t, "[director]\nadapter = \"fake\"\n", "/exit")
			code, out, stderr := runDirectorEnv(t, c.getenv, "start", "--copy", dir)
			if code != 0 {
				t.Fatalf("exit %d\n%s\n%s", code, out, stderr)
			}
			if got := sessions(t, dir); len(got) != 1 || got[0].Pane != (store.DirectorPane{}) {
				t.Errorf("sessions = %+v; want no pane", got)
			}
			want := "director start: warning: this session has no Herdr pane (" + c.reason +
				"), so messages will not wake the Director; start it with --tab or inside a Herdr pane\n"
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
		})
	}
}

// TestDirectorStartTabIgnoresHerdrEnv: the wrapper runs in the Guildmaster's pane, so its HERDR_* binds nothing and
// the session in the tab takes its pane from the flags typed into it.
func TestDirectorStartTabIgnoresHerdrEnv(t *testing.T) {
	dir := tabCopy(t, "/exit")
	host := newDirectorTabs()
	host.onRun = func(tab adapter.TabRef, _ string) {
		if code, out, stderr := runDirectorEnv(t, herdrEnv(nil), supervisorArgv(t, dir, tab)[2:]...); code != 0 {
			t.Errorf("supervisor in the tab: exit %d\n%s\n%s", code, out, stderr)
		}
	}
	app, stdout, stderr := supervisorApp(nil, time.Hour)
	app.Stdin = strings.NewReader("")
	app.Getenv = herdrEnv(nil)
	app.DirectorTabs = func(context.Context, string) (adapter.TabHost, string, error) { return host, "0.9.5", nil }
	code := app.Run([]string{"director", "start", "--tab", "--copy", dir})
	host.runs.Wait()
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	tab := host.tabs[0]
	if got := sessions(t, dir); len(got) != 1 || got[0].Pane != (store.DirectorPane{Session: "lab", Workspace: tab.WorkspaceID, Tab: tab.TabID, Pane: tab.PaneID}) {
		t.Errorf("sessions = %+v; want the tab's pane %+v", got, tab)
	}
	if strings.Contains(stdout.String(), "from HERDR_*") || strings.Contains(stderr.String(), "no Herdr pane") {
		t.Errorf("wrapper used its environment:\n%s\n%s", stdout, stderr)
	}
}

func TestDirectorReloadKeepsTheAdoptedPane(t *testing.T) {
	original := tabCopy(t, "guildhall director ready")
	dir, err := os.MkdirTemp("", "gt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.CopyFS(dir, os.DirFS(original)); err != nil {
		t.Fatal(err)
	}
	input, typing, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close(); _ = typing.Close() }()
	app, stdout, stderr := supervisorApp(input, time.Millisecond)
	app.Getenv = herdrEnv(nil)
	done := make(chan int, 1)
	go func() { done <- app.Run([]string{"director", "start", "--copy", dir}) }()
	waitSessions(t, dir, 1)
	copyRun(t, dir, "director", "reload")
	copyRun(t, dir, "director", "turn-ended")
	got := waitSessions(t, dir, 2)
	_, _ = typing.WriteString("/exit\n")
	if code := <-done; code != 0 {
		t.Errorf("supervisor exit %d\n%s\n%s", code, stdout, stderr)
	}
	if got[0].Pane != herdrPane || got[1].Pane != herdrPane {
		t.Errorf("sessions = %+v; want both in %+v", got, herdrPane)
	}
}

// TestDirectorTabCleanupLeavesAnAdoptedTab: a stopped session's adopted pane is a tab Guildhall did not create, so
// the wrapper's cleanup leaves it open, idle in the replacement's workspace, while closing a stopped flag-bound one.
func TestDirectorTabCleanupLeavesAnAdoptedTab(t *testing.T) {
	dir := tabCopy(t, "/exit")
	adopted := adapter.TabRef{Session: "lab", WorkspaceID: "w3", TabID: "w3:t1", PaneID: "w3:pG"}
	bound := adapter.TabRef{Session: "lab", WorkspaceID: "w3", TabID: "w3:t2", PaneID: "w3:p2"}
	replacement := adapter.TabRef{Session: "lab", WorkspaceID: "w3", TabID: "w3:t3", PaneID: "w3:p3"}
	if code, out, stderr := runDirectorEnv(t, herdrEnv(nil), "start", "--copy", dir); code != 0 {
		t.Fatalf("adopted start: exit %d\n%s\n%s", code, out, stderr)
	}
	if code, out, stderr := runDirectorEnv(t, nil, supervisorArgv(t, dir, bound)[2:]...); code != 0 {
		t.Fatalf("bound start: exit %d\n%s\n%s", code, out, stderr)
	}
	host := newDirectorTabs()
	host.tabs = []adapter.TabRef{adopted, bound, replacement}
	app, _, errout := newTestApp(nil, nil)
	app.retireDirectorTabs(context.Background(), host, filepath.Join(dir, "director.store"), 2, replacement)
	if len(host.closed) != 1 || host.closed[0] != bound {
		t.Errorf("closed = %+v; want only %+v\n%s", host.closed, bound, errout)
	}
}

// noPaneItems is the no_pane items of the copy's director status.
func noPaneItems(t *testing.T, dir string) []needsMeItem {
	t.Helper()
	code, out, stderr := directorCmd(t, nil, "status", "--json", "--copy", dir)
	if code != 0 {
		t.Fatalf("status: exit %d, %s", code, stderr)
	}
	var st directorStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	var items []needsMeItem
	for _, it := range st.NeedsMe {
		if it.Kind == noPaneKind {
			items = append(items, it)
		}
	}
	return items
}

func TestDirectorStatusWarnsOfASessionWithNoPane(t *testing.T) {
	for _, c := range []struct {
		name  string
		pane  store.DirectorPane
		moves []store.DirectorState
		want  bool
	}{
		{"recovering without pane", store.DirectorPane{}, nil, true},
		{"running without pane", store.DirectorPane{}, []store.DirectorState{store.DirectorRunning}, true},
		{"running with pane", herdrPane, []store.DirectorState{store.DirectorRunning}, false},
		{"paused without pane", store.DirectorPane{}, []store.DirectorState{store.DirectorRunning, store.DirectorPaused}, false},
		{"stopped without pane", store.DirectorPane{}, []store.DirectorState{store.DirectorStopped}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := directorCopy(t, "")
			withTestStore(t, dir, func(s *store.Store) (store.Receipt, error) {
				r, err := s.StartDirectorSession(store.DirectorSession{Number: 1, State: store.DirectorRecovering, Identity: store.ProcessIdentity{PID: 7, Token: "boot:7"},
					Release: "dev", ConfigDigest: "sha256:" + strings.Repeat("c", 64), Pane: c.pane})
				for _, to := range c.moves {
					if err != nil {
						return r, err
					}
					var d store.DirectorSession
					if d, err = s.DirectorSession(1); err == nil {
						r, err = s.SetDirectorState(1, to, d.StateSequence)
					}
				}
				return r, err
			})
			items := noPaneItems(t, dir)
			switch {
			case !c.want && len(items) != 0:
				t.Errorf("needs_me no_pane = %+v; want none", items)
			case c.want && (len(items) != 1 || items[0].Command != "guildhall director start --tab" ||
				!strings.Contains(items[0].Reason, "Director session 1 has no Herdr pane, so no wake can reach it; messages wait until its next turn")):
				t.Errorf("needs_me no_pane = %+v; want one naming session 1 and director start --tab", items)
			}
		})
	}
}
