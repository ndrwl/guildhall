package herdr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// call is one recorded invocation of the Herdr executable.
type call struct {
	args []string
	env  []string
}

// fakeRunner replays canned responses keyed by the Herdr subcommand words and
// records every call, so tests check the exact argument lists sent.
type fakeRunner struct {
	t         *testing.T
	calls     []call
	responses map[string]response
	hung      atomic.Bool
}

// hanging reports whether a hanging response has been entered.
func (f *fakeRunner) hanging() bool { return f.hung.Load() }

type response struct {
	stdout string
	stderr string
	exit   int
	// hang blocks until the context ends; echo prints the arguments and the
	// named environment entries; startErr fails as a missing program would.
	hang     bool
	echo     []string
	startErr bool
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// run answers like execRun: canned output and exit code, an error only for a
// program that cannot start or a call whose context ended.
func (f *fakeRunner) run(ctx context.Context, argv, env []string) ([]byte, []byte, int, error) {
	f.calls = append(f.calls, call{args: argv, env: env})
	key := strings.Join(argv[1:3], " ")
	r, ok := f.responses[key]
	if !ok {
		f.t.Fatalf("unexpected herdr call %q", argv)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, -1, fmt.Errorf("herdr stopped: %w", err)
	}
	switch {
	case r.startErr:
		return nil, nil, -1, fmt.Errorf("exec: %q: %w", argv[0], exec.ErrNotFound)
	case r.hang:
		f.hung.Store(true)
		<-ctx.Done()
		return []byte(r.stdout), []byte(r.stderr), -1, fmt.Errorf("herdr stopped: %w", ctx.Err())
	case r.echo != nil:
		return []byte(echoJSON(argv[3:], env, r.echo)), []byte(r.stderr), r.exit, nil
	}
	return []byte(r.stdout), []byte(r.stderr), r.exit, nil
}

func newTestClient(t *testing.T, responses map[string]response) (*Client, *fakeRunner) {
	t.Helper()
	f := &fakeRunner{t: t, responses: responses}
	c, err := New(Options{Session: "guildhall-lab", Command: []string{"herdr"}})
	if err != nil {
		t.Fatal(err)
	}
	c.run = f.run
	return c, f
}

func TestNewRefusesMissingOrDefaultSession(t *testing.T) {
	for _, s := range []string{"", "default", "-x", "has space", "a/b", "--session"} {
		if _, err := New(Options{Session: s, Command: []string{"herdr"}}); err == nil {
			t.Errorf("New(session %q) succeeded, want refusal", s)
		}
	}
	if _, err := New(Options{Session: "lab", Command: nil}); err == nil {
		t.Error("New with no command succeeded, want refusal")
	}
	if _, err := New(Options{Session: "lab-1_x", Command: []string{"herdr"}}); err != nil {
		t.Errorf("New(lab-1_x) = %v", err)
	}
}

func TestEveryCallCarriesExplicitSession(t *testing.T) {
	c, f := newTestClient(t, map[string]response{
		"workspace list": {stdout: fixture(t, "workspace_list_empty.json")},
	})
	c.environ = func() []string {
		return []string{"PATH=/bin", "HERDR_SESSION=default", "HERDR_SOCKET_PATH=/other.sock", "HERDR_PANE_ID=w1:p1"}
	}
	if _, err := c.WorkspaceList(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.calls[0]
	want := []string{"herdr", "workspace", "list", "--session", "guildhall-lab"}
	if !slices.Equal(got.args, want) {
		t.Errorf("argv = %q, want %q", got.args, want)
	}
	if !slices.Contains(got.env, "HERDR_SESSION=guildhall-lab") {
		t.Errorf("env %q lacks HERDR_SESSION=guildhall-lab", got.env)
	}
	for _, e := range got.env {
		if strings.HasPrefix(e, "HERDR_SESSION=default") || strings.HasPrefix(e, "HERDR_SOCKET_PATH=") || strings.HasPrefix(e, "HERDR_PANE_ID=") {
			t.Errorf("ambient %q reached the Herdr call", e)
		}
	}
}

func TestCommandPrefixIsKept(t *testing.T) {
	f := &fakeRunner{t: t}
	c, err := New(Options{Session: "lab", Command: []string{"bash", "wrapper.sh"}})
	if err != nil {
		t.Fatal(err)
	}
	c.run = func(_ context.Context, argv, env []string) ([]byte, []byte, int, error) {
		f.calls = append(f.calls, call{args: argv})
		return []byte(fixture(t, "workspace_list_empty.json")), nil, 0, nil
	}
	if _, err := c.WorkspaceList(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"bash", "wrapper.sh", "workspace", "list", "--session", "lab"}
	if !slices.Equal(f.calls[0].args, want) {
		t.Errorf("argv = %q, want %q", f.calls[0].args, want)
	}
}

func TestStatusChecksMinimumVersionAndRunningServer(t *testing.T) {
	c, _ := newTestClient(t, map[string]response{"status --json": {stdout: fixture(t, "status.json")}})
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status = %v", err)
	}
	if st.ClientVersion != "0.9.5" || st.ServerVersion != "0.9.5" || !st.Running {
		t.Errorf("status = %+v", st)
	}

	old := strings.ReplaceAll(fixture(t, "status.json"), `"version": "0.9.5"`, `"version": "0.9.4"`)
	c, _ = newTestClient(t, map[string]response{"status --json": {stdout: old}})
	if _, err := c.Status(context.Background()); !errors.Is(err, ErrUnsupportedVersion) {
		t.Errorf("Status with 0.9.4 = %v, want ErrUnsupportedVersion", err)
	}

	stopped := strings.Replace(fixture(t, "status.json"), `"running": true`, `"running": false`, 1)
	c, _ = newTestClient(t, map[string]response{"status --json": {stdout: stopped}})
	if _, err := c.Status(context.Background()); !errors.Is(err, ErrServerNotRunning) {
		t.Errorf("Status with stopped server = %v, want ErrServerNotRunning", err)
	}

	other := strings.ReplaceAll(fixture(t, "status.json"), `"session": "guildhall-lab"`, `"session": "default"`)
	c, _ = newTestClient(t, map[string]response{"status --json": {stdout: other}})
	if _, err := c.Status(context.Background()); err == nil {
		t.Error("Status reporting another session succeeded, want refusal")
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"0.9.5", "0.9.5", 0, true},
		{"0.10.0", "0.9.5", 1, true},
		{"1.0.0", "0.9.5", 1, true},
		{"0.9.4", "0.9.5", -1, true},
		{"0.9.5-rc.1", "0.9.5", -1, true},
		{"herdr", "0.9.5", 0, false},
		{"0.9", "0.9.5", 0, false},
	}
	for _, tc := range cases {
		got, err := compareVersions(tc.a, tc.b)
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Errorf("compareVersions(%q, %q) = %d, %v; want %d ok=%v", tc.a, tc.b, got, err, tc.want, tc.ok)
		}
	}
}

func TestWorkspaceCreateAndList(t *testing.T) {
	c, f := newTestClient(t, map[string]response{
		"workspace create": {stdout: fixture(t, "workspace_create.json")},
		"workspace list":   {stdout: fixture(t, "workspace_list_empty.json")},
	})
	ws, err := c.WorkspaceCreate(context.Background(), "guild", "/work")
	if err != nil {
		t.Fatal(err)
	}
	if ws.InitialTab.ID() != "w1:t1" || ws.InitialTab.PaneID() != "w1:p1" || ws.InitialTab.WorkspaceID() != ws.ID || ws.InitialTab.Session() != c.Session() {
		t.Errorf("starting shell ownership = %+v", ws.InitialTab)
	}
	if ws.ID != "w1" || ws.Label != "gh-probe" {
		t.Errorf("workspace = %+v", ws)
	}
	want := []string{"herdr", "workspace", "create", "--label", "guild", "--cwd", "/work", "--no-focus", "--session", "guildhall-lab"}
	if !slices.Equal(f.calls[0].args, want) {
		t.Errorf("argv = %q, want %q", f.calls[0].args, want)
	}
	list, err := c.WorkspaceList(context.Background())
	if err != nil || len(list) != 0 {
		t.Errorf("WorkspaceList = %v, %v", list, err)
	}
}

func TestWorkspaceCreateRefusesAmbiguousStartingShell(t *testing.T) {
	for _, wire := range []string{
		`{"result":{"workspace":{"workspace_id":"w1"}}}`,
		`{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"w1:t1","workspace_id":"w2"},"root_pane":{"pane_id":"w1:p1","tab_id":"w1:t1","workspace_id":"w1"}}}`,
		`{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"w1:t1","workspace_id":"w1"},"root_pane":{"pane_id":"w1:p1","tab_id":"w1:t2","workspace_id":"w1"}}}`,
	} {
		c, _ := newTestClient(t, map[string]response{"workspace create": {stdout: wire}})
		if _, err := c.WorkspaceCreate(context.Background(), "director", "/work"); err == nil {
			t.Errorf("ambiguous response accepted: %s", wire)
		}
	}
}

func TestTabCreateReturnsOwnedTab(t *testing.T) {
	c, f := newTestClient(t, map[string]response{"tab create": {stdout: fixture(t, "tab_create.json")}})
	tab, err := c.TabCreate(context.Background(), TabSpec{
		WorkspaceID: "w1", Label: "artisan-probe", Cwd: "/tmp", Env: []string{"A=1", "B=two words"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tab.ID() != "w1:t2" || tab.PaneID() != "w1:p2" || tab.WorkspaceID() != "w1" || tab.Session() != "guildhall-lab" {
		t.Errorf("tab = %+v", tab)
	}
	want := []string{"herdr", "tab", "create", "--workspace", "w1", "--label", "artisan-probe", "--cwd", "/tmp",
		"--env", "A=1", "--env", "B=two words", "--no-focus", "--session", "guildhall-lab"}
	if !slices.Equal(f.calls[0].args, want) {
		t.Errorf("argv = %q, want %q", f.calls[0].args, want)
	}
}

func TestTabCreateRefusesBadInput(t *testing.T) {
	c, f := newTestClient(t, nil)
	for _, spec := range []TabSpec{
		{WorkspaceID: "", Label: "x"},
		{WorkspaceID: "w1", Label: "x", Env: []string{"NOEQUALS"}},
		{WorkspaceID: "w1", Label: "x", Env: []string{"=v"}},
		{WorkspaceID: "-w1", Label: "x"},
	} {
		if _, err := c.TabCreate(context.Background(), spec); err == nil {
			t.Errorf("TabCreate(%+v) succeeded, want refusal", spec)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("refused input still called herdr: %v", f.calls)
	}
}

func TestTabCreateRejectsResponseFromOtherWorkspace(t *testing.T) {
	c, _ := newTestClient(t, map[string]response{"tab create": {stdout: fixture(t, "tab_create.json")}})
	if _, err := c.TabCreate(context.Background(), TabSpec{WorkspaceID: "w7", Label: "x"}); err == nil {
		t.Error("TabCreate accepted a tab in another workspace")
	}
}

func TestAPIErrorIsParsed(t *testing.T) {
	c, _ := newTestClient(t, map[string]response{"tab get": {stderr: fixture(t, "error_tab_not_found.json"), exit: 1}})
	_, err := c.TabGet(context.Background(), "w9:t9")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "tab_not_found" {
		t.Fatalf("TabGet error = %v, want APIError tab_not_found", err)
	}
	if !IsNotFound(err) {
		t.Error("IsNotFound(tab_not_found) = false")
	}
}

func TestUsageAndUnparsableFailures(t *testing.T) {
	c, _ := newTestClient(t, map[string]response{
		"tab get":  {stderr: "error: unexpected argument", exit: 2},
		"tab list": {stdout: "not json"},
	})
	if _, err := c.TabGet(context.Background(), "w1:t1"); err == nil || IsNotFound(err) {
		t.Errorf("usage failure = %v", err)
	}
	if _, err := c.TabList(context.Background(), "w1"); err == nil {
		t.Error("unparsable output accepted")
	}
}

func TestTabListAndGet(t *testing.T) {
	c, f := newTestClient(t, map[string]response{
		"tab list": {stdout: fixture(t, "tab_list.json")},
		"tab get":  {stdout: fixture(t, "tab_get.json")},
	})
	tabs, err := c.TabList(context.Background(), "w1")
	if err != nil || len(tabs) != 2 || tabs[1].ID != "w1:t2" || tabs[1].Label != "artisan-probe" {
		t.Fatalf("TabList = %+v, %v", tabs, err)
	}
	if want := []string{"herdr", "tab", "list", "--workspace", "w1", "--session", "guildhall-lab"}; !slices.Equal(f.calls[0].args, want) {
		t.Errorf("argv = %q", f.calls[0].args)
	}
	tab, err := c.TabGet(context.Background(), "w1:t2")
	if err != nil || tab.ID != "w1:t2" || tab.WorkspaceID != "w1" {
		t.Errorf("TabGet = %+v, %v", tab, err)
	}
}

func TestCloseTabOnlyOwnedBySameSession(t *testing.T) {
	c, f := newTestClient(t, map[string]response{
		"tab create": {stdout: fixture(t, "tab_create.json")},
		"tab close":  {stdout: fixture(t, "tab_close.json")},
	})
	tab, err := c.TabCreate(context.Background(), TabSpec{WorkspaceID: "w1", Label: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CloseOwnedTab(context.Background(), OwnedTab{}); err == nil {
		t.Error("closing a zero OwnedTab succeeded")
	}
	other, err := New(Options{Session: "other-lab", Command: []string{"herdr"}})
	if err != nil {
		t.Fatal(err)
	}
	other.run = f.run
	if err := other.CloseOwnedTab(context.Background(), tab); err == nil {
		t.Error("another session's client closed the tab")
	}
	if err := c.CloseOwnedTab(context.Background(), tab); err != nil {
		t.Fatal(err)
	}
	last := f.calls[len(f.calls)-1].args
	if want := []string{"herdr", "tab", "close", "w1:t2", "--session", "guildhall-lab"}; !slices.Equal(last, want) {
		t.Errorf("argv = %q, want %q", last, want)
	}
}

func TestCloseOwnedWorkspace(t *testing.T) {
	c, f := newTestClient(t, map[string]response{
		"tab create":      {stdout: fixture(t, "tab_create.json")},
		"workspace close": {stdout: fixture(t, "workspace_close.json")},
	})
	c.environ = func() []string {
		return []string{"PATH=/bin", "HERDR_SESSION=default", "HERDR_SOCKET_PATH=/other.sock", "HERDR_WORKSPACE_ID=w9"}
	}
	tab, err := c.TabCreate(context.Background(), TabSpec{WorkspaceID: "w1", Label: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CloseOwnedWorkspace(context.Background(), OwnedTab{}, "w1"); err == nil {
		t.Error("closing a workspace with a zero OwnedTab succeeded")
	}
	if err := c.CloseOwnedWorkspace(context.Background(), tab, "w2"); err == nil {
		t.Error("closing a workspace the owned tab is not in succeeded")
	}
	if err := c.CloseOwnedWorkspace(context.Background(), tab, "--all"); err == nil {
		t.Error("closing an option-shaped workspace id succeeded")
	}
	other, err := New(Options{Session: "other-lab", Command: []string{"herdr"}})
	if err != nil {
		t.Fatal(err)
	}
	other.run = f.run
	if err := other.CloseOwnedWorkspace(context.Background(), tab, "w1"); err == nil {
		t.Error("another session's client closed the workspace")
	}
	if len(f.calls) != 1 {
		t.Fatalf("refused closes reached Herdr: %q", f.calls)
	}
	if err := c.CloseOwnedWorkspace(context.Background(), tab, "w1"); err != nil {
		t.Fatal(err)
	}
	last := f.calls[len(f.calls)-1]
	if want := []string{"herdr", "workspace", "close", "w1", "--session", "guildhall-lab"}; !slices.Equal(last.args, want) {
		t.Errorf("argv = %q, want %q", last.args, want)
	}
	for _, e := range last.env {
		if strings.HasPrefix(e, "HERDR_SESSION=default") || strings.HasPrefix(e, "HERDR_SOCKET_PATH=") || strings.HasPrefix(e, "HERDR_WORKSPACE_ID=") {
			t.Errorf("ambient %q reached the Herdr call", e)
		}
	}
	if _, err := New(Options{Session: "default", Command: []string{"herdr"}}); err == nil {
		t.Error("a client for the default session was built")
	}
}

func TestPaneProcessInfo(t *testing.T) {
	c, f := newTestClient(t, map[string]response{"pane process-info": {stdout: fixture(t, "process_info_idle.json")}})
	info, err := c.PaneProcessInfo(context.Background(), "w1:p2")
	if err != nil {
		t.Fatal(err)
	}
	if info.ShellPID != 1204400 || len(info.Foreground) != 1 || info.Foreground[0].Name != "zsh" {
		t.Errorf("info = %+v", info)
	}
	if !info.ShellIdle() {
		t.Error("idle shell pane not reported idle")
	}
	if want := []string{"herdr", "pane", "process-info", "--pane", "w1:p2", "--session", "guildhall-lab"}; !slices.Equal(f.calls[0].args, want) {
		t.Errorf("argv = %q", f.calls[0].args)
	}

	c, _ = newTestClient(t, map[string]response{"pane process-info": {stdout: fixture(t, "process_info_busy.json")}})
	info, err = c.PaneProcessInfo(context.Background(), "w1:p2")
	if err != nil {
		t.Fatal(err)
	}
	if info.ShellIdle() {
		t.Error("pane running sleep reported idle")
	}
}

func TestPaneRunAndInterruptUseOwnedPane(t *testing.T) {
	c, f := newTestClient(t, map[string]response{
		"tab create":     {stdout: fixture(t, "tab_create.json")},
		"pane run":       {},
		"pane send-keys": {},
	})
	tab, err := c.TabCreate(context.Background(), TabSpec{WorkspaceID: "w1", Label: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RunInOwnedTab(context.Background(), tab, "echo hi"); err != nil {
		t.Fatal(err)
	}
	if err := c.InterruptOwnedTab(context.Background(), tab); err != nil {
		t.Fatal(err)
	}
	if want := []string{"herdr", "pane", "run", "w1:p2", "echo hi", "--session", "guildhall-lab"}; !slices.Equal(f.calls[1].args, want) {
		t.Errorf("run argv = %q", f.calls[1].args)
	}
	if want := []string{"herdr", "pane", "send-keys", "w1:p2", "ctrl+c", "--session", "guildhall-lab"}; !slices.Equal(f.calls[2].args, want) {
		t.Errorf("send-keys argv = %q", f.calls[2].args)
	}
	if err := c.RunInOwnedTab(context.Background(), OwnedTab{}, "x"); err == nil {
		t.Error("RunInOwnedTab accepted a zero tab")
	}
	if err := c.RunInOwnedTab(context.Background(), tab, "two\nlines"); err == nil {
		t.Error("RunInOwnedTab accepted a multi-line command")
	}
}

// TestPaneTitleUsesOwnedPane: the wake notice is the pane's title, set and cleared only on a pane this client
// owns (d8).
func TestPaneTitleUsesOwnedPane(t *testing.T) {
	c, f := newTestClient(t, map[string]response{
		"tab create":           {stdout: fixture(t, "tab_create.json")},
		"pane report-metadata": {},
	})
	tab, err := c.TabCreate(context.Background(), TabSpec{WorkspaceID: "w1", Label: "a"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.PaneTitle(ctx, tab, "guildhall", "guildhall: 2 unread (note 2)"); err != nil {
		t.Fatal(err)
	}
	if err := c.PaneTitle(ctx, tab, "guildhall", ""); err != nil {
		t.Fatal(err)
	}
	for i, want := range [][]string{
		{"herdr", "pane", "report-metadata", "w1:p2", "--source", "guildhall", "--title", "guildhall: 2 unread (note 2)", "--session", "guildhall-lab"},
		{"herdr", "pane", "report-metadata", "w1:p2", "--source", "guildhall", "--clear-title", "--session", "guildhall-lab"},
	} {
		if !slices.Equal(f.calls[i+1].args, want) {
			t.Errorf("call %d argv = %q, want %q", i+1, f.calls[i+1].args, want)
		}
	}
	if err := c.PaneTitle(ctx, OwnedTab{}, "guildhall", "x"); err == nil {
		t.Error("PaneTitle accepted a zero tab")
	}
	if err := c.PaneTitle(ctx, tab, "guildhall", "two\nlines"); err == nil {
		t.Error("PaneTitle accepted a multi-line title")
	}
	if len(f.calls) != 3 {
		t.Errorf("refusals reached Herdr: %d calls", len(f.calls))
	}
}

func TestReportAgentRegistersTheResumeCommand(t *testing.T) {
	c, f := newTestClient(t, map[string]response{"pane report-agent": {stdout: `{"id":"cli:request","result":{"type":"ok"}}`}})
	resume := []string{"guildhall", "artisan", "resume", "--store", "/s/g.store", "--state", "/s/st", "--attempt", "1"}
	if err := c.ReportAgent(context.Background(), "w1:p2", AgentReport{Source: "guildhall", Agent: "artisan", State: "working", Resume: resume}); err != nil {
		t.Fatal(err)
	}
	want := append([]string{"herdr", "pane", "report-agent", "--source", "guildhall", "--agent", "artisan", "--state", "working", "w1:p2", "--session", "guildhall-lab", "--"}, resume...)
	if len(f.calls) != 1 || !slices.Equal(f.calls[0].args, want) {
		t.Fatalf("argv = %q, want %q", f.calls, want)
	}
	if err := c.ReportAgent(context.Background(), "w1:p2", AgentReport{Source: "guildhall", Agent: "artisan", State: "idle"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"herdr", "pane", "report-agent", "--source", "guildhall", "--agent", "artisan", "--state", "idle", "w1:p2", "--session", "guildhall-lab"}; !slices.Equal(f.calls[1].args, want) {
		t.Errorf("argv without resume = %q, want %q", f.calls[1].args, want)
	}
}

func TestReportAgentRefusesWhatHerdrWouldReject(t *testing.T) {
	good := AgentReport{Source: "guildhall", Agent: "artisan", State: "working", Resume: []string{"guildhall"}}
	for name, tc := range map[string]struct {
		pane string
		r    AgentReport
		want error
	}{
		"absolute resume command": {"w1:p2", AgentReport{Source: "guildhall", Agent: "artisan", State: "working", Resume: []string{"/usr/local/bin/guildhall", "artisan"}}, ErrResumeArgv},
		"relative resume command": {"w1:p2", AgentReport{Source: "guildhall", Agent: "artisan", State: "working", Resume: []string{"./guildhall"}}, ErrResumeArgv},
		"windows resume command":  {"w1:p2", AgentReport{Source: "guildhall", Agent: "artisan", State: "working", Resume: []string{`C:\bin\guildhall.exe`}}, ErrResumeArgv},
		"option resume command":   {"w1:p2", AgentReport{Source: "guildhall", Agent: "artisan", State: "working", Resume: []string{"--session"}}, ErrResumeArgv},
		"empty resume command":    {"w1:p2", AgentReport{Source: "guildhall", Agent: "artisan", State: "working", Resume: []string{""}}, ErrResumeArgv},
		"bad state":               {"w1:p2", AgentReport{Source: "guildhall", Agent: "artisan", State: "done"}, nil},
		"bad source":              {"w1:p2", AgentReport{Source: "--x", Agent: "artisan", State: "idle"}, nil},
		"empty agent":             {"w1:p2", AgentReport{Source: "guildhall", State: "idle"}, nil},
		"bad pane":                {"--session", good, nil},
	} {
		t.Run(name, func(t *testing.T) {
			c, f := newTestClient(t, nil)
			err := c.ReportAgent(context.Background(), tc.pane, tc.r)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Errorf("ReportAgent = %v, want a refusal %v", err, tc.want)
			}
			if len(f.calls) != 0 {
				t.Errorf("a refused report called herdr: %q", f.calls)
			}
		})
	}
}

func TestReportAgentReturnsHerdrsRefusal(t *testing.T) {
	c, _ := newTestClient(t, map[string]response{"pane report-agent": {exit: 1, stderr: `{"error":{"code":"invalid_resume_argv","message":"resume_argv must start with a plain command name, not a path"},"id":"cli:request"}`}})
	err := c.ReportAgent(context.Background(), "w1:p2", AgentReport{Source: "guildhall", Agent: "artisan", State: "working", Resume: []string{"guildhall"}})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "invalid_resume_argv" {
		t.Errorf("ReportAgent = %v, want Herdr's invalid_resume_argv", err)
	}
}

func TestRecordedTabChecksTheTabStillExists(t *testing.T) {
	c, f := newTestClient(t, map[string]response{
		"tab get":  {stdout: fixture(t, "tab_get.json")},
		"pane run": {},
	})
	tab, err := c.RecordedTab(context.Background(), "w1", "w1:t2", "w1:p2")
	if err != nil {
		t.Fatal(err)
	}
	if tab.Session() != "guildhall-lab" || tab.WorkspaceID() != "w1" || tab.ID() != "w1:t2" || tab.PaneID() != "w1:p2" {
		t.Errorf("tab = %+v", tab)
	}
	if want := []string{"herdr", "tab", "get", "w1:t2", "--session", "guildhall-lab"}; !slices.Equal(f.calls[0].args, want) {
		t.Errorf("argv = %q, want %q", f.calls[0].args, want)
	}
	if err := c.RunInOwnedTab(context.Background(), tab, "guildhall: 1 unread (note 1)"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"herdr", "pane", "run", "w1:p2", "guildhall: 1 unread (note 1)", "--session", "guildhall-lab"}; !slices.Equal(f.calls[1].args, want) {
		t.Errorf("run argv = %q", f.calls[1].args)
	}
	if _, err := c.RecordedTab(context.Background(), "w9", "w1:t2", "w1:p2"); err == nil {
		t.Error("RecordedTab accepted a tab now in another workspace")
	}
	for _, ids := range [][3]string{{"", "w1:t2", "w1:p2"}, {"w1", "-t", "w1:p2"}, {"w1", "w1:t2", ""}} {
		calls := len(f.calls)
		if _, err := c.RecordedTab(context.Background(), ids[0], ids[1], ids[2]); err == nil || len(f.calls) != calls {
			t.Errorf("RecordedTab%q = %v after %d calls; want a refusal sending nothing", ids, err, len(f.calls)-calls)
		}
	}

	gone, _ := newTestClient(t, map[string]response{"tab get": {stderr: fixture(t, "error_tab_not_found.json"), exit: 1}})
	if _, err := gone.RecordedTab(context.Background(), "w9", "w9:t9", "w9:p9"); !IsNotFound(err) {
		t.Errorf("RecordedTab of a closed tab = %v, want not found", err)
	}
}
