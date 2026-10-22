package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/skills"
)

// directorTabs is the terminal host double for the Director's wrapper: workspace w1, labelled guildmaster,
// exists from the start; each tab's shell is bash; and onRun sees each typed line after the pane turns busy.
// After onRun returns the pane reports busy for busyReads more reads and then idle, as a shell whose command
// has ended.
type directorTabs struct {
	onRun     func(tab adapter.TabRef, line string)
	busyReads int

	mu         sync.Mutex
	workspaces []herdr.Workspace
	created    []herdr.TabSpec
	tabs       []adapter.TabRef
	busy       map[string]int
	sent       []string
	runs       sync.WaitGroup
}

func newDirectorTabs() *directorTabs {
	return &directorTabs{workspaces: []herdr.Workspace{{ID: "w1", Label: "guildmaster"}}, busy: map[string]int{}}
}

func (d *directorTabs) Workspaces(ctx context.Context) ([]herdr.Workspace, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.workspaces), nil
}

func (d *directorTabs) CreateWorkspace(ctx context.Context, label, cwd string) (herdr.Workspace, error) {
	if err := ctx.Err(); err != nil {
		return herdr.Workspace{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	w := herdr.Workspace{ID: fmt.Sprintf("w%d", len(d.workspaces)+1), Label: label}
	d.workspaces = append(d.workspaces, w)
	return w, nil
}

func (d *directorTabs) CreateTab(ctx context.Context, spec herdr.TabSpec) (adapter.TabRef, error) {
	if err := ctx.Err(); err != nil {
		return adapter.TabRef{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !slices.ContainsFunc(d.workspaces, func(w herdr.Workspace) bool { return w.ID == spec.WorkspaceID }) {
		return adapter.TabRef{}, &herdr.APIError{Code: "workspace_not_found", Message: "workspace not found"}
	}
	d.created = append(d.created, spec)
	n := len(d.created) + 1
	ref := adapter.TabRef{Session: "lab", WorkspaceID: spec.WorkspaceID, TabID: fmt.Sprintf("%s:t%d", spec.WorkspaceID, n), PaneID: fmt.Sprintf("%s:p%d", spec.WorkspaceID, n)}
	d.tabs = append(d.tabs, ref)
	return ref, nil
}

func (d *directorTabs) AdoptTab(ctx context.Context, ref adapter.TabRef) error {
	return errors.New("the wrapper adopts no tab")
}

func (d *directorTabs) PaneProcessInfo(ctx context.Context, ref adapter.TabRef) (herdr.ProcessInfo, error) {
	if err := ctx.Err(); err != nil {
		return herdr.ProcessInfo{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !slices.Contains(d.tabs, ref) {
		return herdr.ProcessInfo{}, errors.New("not a tab of this host")
	}
	switch n := d.busy[ref.PaneID]; {
	case n < 0:
		return herdr.ProcessInfo{ShellPID: 10, Foreground: []herdr.Process{{PID: 11, Name: "guildhall"}}}, nil
	case n > 0:
		d.busy[ref.PaneID]--
		return herdr.ProcessInfo{ShellPID: 10, Foreground: []herdr.Process{{PID: 11, Name: "guildhall"}}}, nil
	}
	return herdr.ProcessInfo{ShellPID: 10, Foreground: []herdr.Process{{PID: 10, Name: "bash"}}}, nil
}

func (d *directorTabs) RunInTab(ctx context.Context, ref adapter.TabRef, line string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !slices.Contains(d.tabs, ref) {
		return errors.New("not a tab of this host")
	}
	d.sent = append(d.sent, line)
	d.busy[ref.PaneID] = -1
	d.runs.Add(1)
	go func() {
		defer d.runs.Done()
		if d.onRun != nil {
			d.onRun(ref, line)
		}
		d.mu.Lock()
		d.busy[ref.PaneID] = d.busyReads
		d.mu.Unlock()
	}()
	return nil
}

func (d *directorTabs) InterruptTab(ctx context.Context, ref adapter.TabRef) error {
	return errors.New("the wrapper interrupts no tab")
}

// tabCopy is a Director copy named alpha whose [local] herdr_session is lab, playing steps on the fake runtime.
func tabCopy(t *testing.T, steps ...string) string {
	t.Helper()
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n", steps...)
	cfg := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfg, strings.Replace(string(b), "[local]\n", "[local]\nherdr_session = \"lab\"\n", 1))
	return dir
}

// runWrapper runs `guildhall director start <args>` over host, as cmd/guildhall assembles it.
func runWrapper(t *testing.T, host adapter.TabHost, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Executable = os.Executable
	app.LookPath = exec.LookPath
	app.InstallSkills = skills.Install
	app.InstallBaseline = baseline.Install
	app.Stdin = strings.NewReader("")
	app.DirectorTabs = func(_ context.Context, session string) (adapter.TabHost, string, error) {
		if session != "lab" {
			return nil, "", fmt.Errorf("opened Herdr session %q, want lab", session)
		}
		return host, "0.9.5", nil
	}
	code := app.Run(append([]string{"director", "start"}, args...))
	return code, stdout.String(), stderr.String()
}

// supervisorArgv is the command the wrapper types for a session in tab, before quoting.
func supervisorArgv(t *testing.T, dir string, tab adapter.TabRef) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{exe, "director", "start", "--copy", dir,
		"--herdr-session", tab.Session, "--herdr-workspace", tab.WorkspaceID, "--herdr-tab", tab.TabID, "--herdr-pane", tab.PaneID}
}

// lastSession parses the wrapper's last stdout line as the session it printed.
func lastSession(t *testing.T, out string) store.DirectorSession {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var d store.DirectorSession
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &d); err != nil {
		t.Fatalf("last stdout line is not a session: %v\n%s", err, out)
	}
	return d
}

// TestDirectorStartOpensTheDirectorTab runs the wrapper twice: each run creates a tab labelled with the
// Director's name, without focus, in the one workspace labelled alpha, which the first run creates and the
// second reuses; types the supervisor's argv quoted for the pane's shell; and prints the session once its
// record carries that tab's pane.
func TestDirectorStartOpensTheDirectorTab(t *testing.T) {
	dir := tabCopy(t, "/exit")
	host := newDirectorTabs()
	var typed []string
	host.onRun = func(tab adapter.TabRef, line string) {
		argv := supervisorArgv(t, dir, tab)
		want, err := herdr.ShellCommand("bash", argv)
		if err != nil || line != want {
			t.Errorf("typed %q, want %q (%v)", line, want, err)
			return
		}
		typed = append(typed, line)
		if code, out, stderr := runDirectorCmd(t, argv[2:]...); code != 0 {
			t.Errorf("supervisor in the tab: exit %d\n%s\n%s", code, out, stderr)
		}
	}
	for n := 1; n <= 2; n++ {
		code, out, stderr := runWrapper(t, host, "--tab", "--copy", dir)
		host.runs.Wait()
		if code != 0 {
			t.Fatalf("start %d: exit %d\nstdout %s\nstderr %s", n, code, out, stderr)
		}
		for _, want := range []string{"config: ", "runtime: fake", "store: ", "skills: ", `herdr: session "lab"`} {
			if !strings.Contains(out, want) {
				t.Errorf("start %d: stdout lacks %q:\n%s", n, want, out)
			}
		}
		tab := host.tabs[n-1]
		if d := lastSession(t, out); d.Number != n || d.Pane != (store.DirectorPane{Session: "lab", Workspace: tab.WorkspaceID, Tab: tab.TabID, Pane: tab.PaneID}) {
			t.Errorf("start %d printed %+v; want session %d in %+v", n, d, n, tab)
		}
	}
	if len(host.workspaces) != 2 || host.workspaces[1].Label != "alpha" {
		t.Errorf("workspaces = %+v; want guildmaster and one alpha", host.workspaces)
	}
	for _, spec := range host.created {
		if spec.WorkspaceID != host.workspaces[1].ID || spec.Label != "director alpha" || spec.Cwd != dir {
			t.Errorf("tab created = %+v; want director alpha in %s at the copy", spec, host.workspaces[1].ID)
		}
	}
	if len(typed) != 2 || len(host.sent) != 2 {
		t.Errorf("typed %q, sent %q; want one line per start", typed, host.sent)
	}
	if got := sessions(t, dir); len(got) != 2 || got[0].Pane.Tab != host.tabs[0].TabID || got[1].Pane.Tab != host.tabs[1].TabID {
		t.Errorf("sessions = %+v", got)
	}
}

// TestDirectorStartFailsWhenTheTabCommandEnds sees the typed command end with no session recorded, as a refused
// preflight in the tab would: the wrapper exits 1 and leaves the tab open.
func TestDirectorStartFailsWhenTheTabCommandEnds(t *testing.T) {
	dir := tabCopy(t, "/exit")
	host := newDirectorTabs()
	host.busyReads = 1
	code, out, stderr := runWrapper(t, host, "--tab", "--copy", dir)
	host.runs.Wait()
	if code != 1 || !strings.Contains(stderr, "ended before") || !strings.Contains(stderr, host.tabs[0].TabID) {
		t.Fatalf("exit %d, stdout %q, stderr %q; want exit 1 naming the tab whose command ended", code, out, stderr)
	}
	if got := sessions(t, dir); len(got) != 0 {
		t.Errorf("sessions = %+v", got)
	}
}

// TestDirectorStartRefusesALiveSession refuses a second start, in a tab or in the foreground, while the newest
// session is recovering or running under a live supervisor: no tab is opened and nothing is recorded.
func TestDirectorStartRefusesALiveSession(t *testing.T) {
	for _, state := range []store.DirectorState{store.DirectorRecovering, store.DirectorRunning} {
		t.Run(string(state), func(t *testing.T) {
			dir := tabCopy(t, "/exit")
			self, err := artisan.Self()
			if err != nil {
				t.Fatal(err)
			}
			withTestStore(t, dir, func(s *store.Store) (store.Receipt, error) {
				r, err := s.StartDirectorSession(store.DirectorSession{Number: 1, State: store.DirectorRecovering, Identity: store.ProcessIdentity(self),
					Release: "dev", ConfigDigest: "sha256:" + strings.Repeat("c", 64)})
				if err != nil || state == store.DirectorRecovering {
					return r, err
				}
				return s.SetDirectorState(1, state, r.Sequence)
			})
			host := newDirectorTabs()
			for _, args := range [][]string{{"--tab", "--copy", dir}, {"--copy", dir}} {
				code, out, stderr := runWrapper(t, host, args...)
				if code != 1 || !strings.Contains(stderr, "director: live") {
					t.Errorf("%q: exit %d, stdout %q, stderr %q; want exit 1 naming director: live", args, code, out, stderr)
				}
			}
			if len(host.created) != 0 || len(host.workspaces) != 1 {
				t.Errorf("a refused start opened %+v in %+v", host.created, host.workspaces)
			}
			if got := sessions(t, dir); len(got) != 1 || got[0].State != state {
				t.Errorf("sessions = %+v", got)
			}
		})
	}
}

// TestDirectorStartSupersedesADeadSession starts over a running session whose supervisor is gone: the old
// session is recorded stopped, superseded, and the new one runs.
func TestDirectorStartSupersedesADeadSession(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n", "/exit")
	withTestStore(t, dir, func(s *store.Store) (store.Receipt, error) {
		// This process's PID with a token it never had: a supervisor that is gone.
		r, err := s.StartDirectorSession(store.DirectorSession{Number: 1, State: store.DirectorRecovering, Identity: store.ProcessIdentity{PID: os.Getpid(), Token: "gone"},
			Release: "dev", ConfigDigest: "sha256:" + strings.Repeat("c", 64)})
		if err != nil {
			return r, err
		}
		return s.SetDirectorState(1, store.DirectorRunning, r.Sequence)
	})
	if code, out, stderr := runDirectorCmd(t, "start", "--copy", dir); code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, stderr)
	}
	got := sessions(t, dir)
	if len(got) != 2 || got[0].State != store.DirectorStopped || got[0].EndReason != store.EndSuperseded ||
		got[1].State != store.DirectorStopped || got[1].EndReason != "" {
		t.Errorf("sessions = %+v; want session 1 superseded and session 2 stopped by its supervisor", got)
	}
}

// TestDirectorStartRecordsItsPane records the pane binding the wrapper passes on the session.
func TestDirectorStartRecordsItsPane(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n", "/exit")
	code, out, stderr := runDirectorCmd(t, "start", "--copy", dir,
		"--herdr-session", "lab", "--herdr-workspace", "w2", "--herdr-tab", "w2:t3", "--herdr-pane", "w2:p3")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, stderr)
	}
	if got := sessions(t, dir); len(got) != 1 || got[0].Pane != (store.DirectorPane{Session: "lab", Workspace: "w2", Tab: "w2:t3", Pane: "w2:p3"}) {
		t.Errorf("sessions = %+v", got)
	}
}
