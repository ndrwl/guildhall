package adapter

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ndrwl/guildhall/internal/herdr"
)

// TabHost is the terminal boundary TabLauncher consumes. testTabHost is its
// contract; it runs against HerdrTabs over a real herdr.Client whose Herdr
// executable is a stub (the test binary), and against the fakeTabs double the
// launcher tests use, so a launcher test that passes on the double means the
// same against the real client.

type tabHostUnderTest struct {
	host TabHost
	// foreign is another host of the same kind; its tabs must be refused.
	foreign TabHost
	session string
	// sent returns the lines and keys that reached the terminal, in order.
	sent func() []string
	// reopen returns a new host on the same terminal, as a later process would
	// make: it holds the tabs host created but owns none of them.
	reopen func() TabHost
}

func testTabHost(t *testing.T, newHost func(t *testing.T) tabHostUnderTest) {
	ctx := context.Background()
	spec := herdr.TabSpec{WorkspaceID: "w1", Label: "artisan t/a", Cwd: t.TempDir()}

	t.Run("create returns a tab in the requested workspace and session", func(t *testing.T) {
		h := newHost(t)
		ref, err := h.host.CreateTab(ctx, spec)
		if err != nil {
			t.Fatal(err)
		}
		if ref.WorkspaceID != "w1" || ref.Session != h.session || ref.TabID == "" || ref.PaneID == "" {
			t.Errorf("ref = %+v", ref)
		}
		again, err := h.host.CreateTab(ctx, spec)
		if err != nil || again.TabID == ref.TabID || again.PaneID == ref.PaneID {
			t.Errorf("second tab = %+v, %v; want distinct ids", again, err)
		}
	})
	t.Run("create in a missing workspace fails", func(t *testing.T) {
		h := newHost(t)
		if _, err := h.host.CreateTab(ctx, herdr.TabSpec{WorkspaceID: "w404", Label: "x"}); err == nil {
			t.Error("CreateTab in a missing workspace succeeded")
		}
	})
	t.Run("a new tab's shell is idle", func(t *testing.T) {
		h := newHost(t)
		ref := mustCreate(t, h.host, spec)
		info, err := h.host.PaneProcessInfo(ctx, ref)
		if err != nil || !info.ShellIdle() || info.Foreground[0].Name == "" {
			t.Errorf("process info = %+v, %v", info, err)
		}
	})
	t.Run("tabs the host did not create are refused and receive nothing", func(t *testing.T) {
		h := newHost(t)
		own := mustCreate(t, h.host, spec)
		foreign := mustCreate(t, h.foreign, spec)
		forged := own
		forged.PaneID += "0"
		for name, ref := range map[string]TabRef{"zero": {}, "foreign": foreign, "forged": forged} {
			if _, err := h.host.PaneProcessInfo(ctx, ref); err == nil {
				t.Errorf("%s: PaneProcessInfo accepted %+v", name, ref)
			}
			if err := h.host.RunInTab(ctx, ref, "echo no"); err == nil {
				t.Errorf("%s: RunInTab accepted %+v", name, ref)
			}
			if err := h.host.InterruptTab(ctx, ref); err == nil {
				t.Errorf("%s: InterruptTab accepted %+v", name, ref)
			}
		}
		if sent := h.sent(); len(sent) != 0 {
			t.Errorf("refused tabs still reached the terminal: %q", sent)
		}
	})
	t.Run("run makes the pane busy and interrupt returns it to the shell", func(t *testing.T) {
		h := newHost(t)
		ref := mustCreate(t, h.host, spec)
		if err := h.host.RunInTab(ctx, ref, "sleep 60"); err != nil {
			t.Fatal(err)
		}
		if info, err := h.host.PaneProcessInfo(ctx, ref); err != nil || info.ShellIdle() {
			t.Errorf("after run: %+v, %v; want a busy pane", info, err)
		}
		if err := h.host.InterruptTab(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if info, err := h.host.PaneProcessInfo(ctx, ref); err != nil || !info.ShellIdle() {
			t.Errorf("after interrupt: %+v, %v; want the shell idle", info, err)
		}
		if want := []string{"run " + ref.PaneID + " sleep 60", "keys " + ref.PaneID + " ctrl+c"}; !slices.Equal(h.sent(), want) {
			t.Errorf("sent = %q, want %q", h.sent(), want)
		}
	})
	t.Run("run refuses anything but one non-empty line", func(t *testing.T) {
		h := newHost(t)
		ref := mustCreate(t, h.host, spec)
		for _, line := range []string{"", "two\nlines", "cr\rline"} {
			if err := h.host.RunInTab(ctx, ref, line); err == nil {
				t.Errorf("RunInTab accepted %q", line)
			}
		}
		if sent := h.sent(); len(sent) != 0 {
			t.Errorf("refused lines reached the terminal: %q", sent)
		}
	})
	t.Run("an ended context fails every operation", func(t *testing.T) {
		h := newHost(t)
		ref := mustCreate(t, h.host, spec)
		done, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := h.host.CreateTab(done, spec); !errors.Is(err, context.Canceled) {
			t.Errorf("CreateTab = %v", err)
		}
		if _, err := h.host.PaneProcessInfo(done, ref); !errors.Is(err, context.Canceled) {
			t.Errorf("PaneProcessInfo = %v", err)
		}
		if err := h.host.RunInTab(done, ref, "x"); !errors.Is(err, context.Canceled) {
			t.Errorf("RunInTab = %v", err)
		}
		if err := h.host.InterruptTab(done, ref); !errors.Is(err, context.Canceled) {
			t.Errorf("InterruptTab = %v", err)
		}
		if sent := h.sent(); len(sent) != 0 {
			t.Errorf("cancelled calls reached the terminal: %q", sent)
		}
	})
}

// testRecordedTabs is the contract for tabs a later host adopts from a durable
// record, and for typing into them, as the Director's wake does.
func testRecordedTabs(t *testing.T, newHost func(t *testing.T) tabHostUnderTest) {
	ctx := context.Background()
	spec := herdr.TabSpec{WorkspaceID: "w1", Label: "director", Cwd: t.TempDir()}
	const wake = "guildhall: 2 unread (note 1, question 1)"

	t.Run("a later host adopts a recorded tab and then operates on it", func(t *testing.T) {
		h := newHost(t)
		ref := mustCreate(t, h.host, spec)
		if err := h.host.RunInTab(ctx, ref, "sleep 60"); err != nil {
			t.Fatal(err)
		}
		later := h.reopen()
		if err := later.RunInTab(ctx, ref, "early"); err == nil {
			t.Fatal("a later host typed into a tab it had not adopted")
		}
		if err := later.AdoptTab(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if info, err := later.PaneProcessInfo(ctx, ref); err != nil || info.ShellIdle() {
			t.Errorf("adopted tab's process info = %+v, %v; want the running program", info, err)
		}
		if err := later.RunInTab(ctx, ref, wake); err != nil {
			t.Fatal(err)
		}
		if want := []string{"run " + ref.PaneID + " sleep 60", "run " + ref.PaneID + " " + wake}; !slices.Equal(h.sent(), want) {
			t.Errorf("sent = %q, want %q", h.sent(), want)
		}
	})
	t.Run("adopt refuses a tab the terminal does not hold in the host's session", func(t *testing.T) {
		h := newHost(t)
		own := mustCreate(t, h.host, spec)
		foreign := mustCreate(t, h.foreign, spec)
		missing, otherWorkspace, otherSession := own, own, own
		missing.TabID, missing.PaneID = "w1:t99", "w1:p99"
		otherWorkspace.WorkspaceID = "w2"
		otherSession.Session = "other-lab"
		later := h.reopen()
		for name, ref := range map[string]TabRef{"zero": {}, "foreign": foreign, "missing": missing, "other workspace": otherWorkspace, "other session": otherSession} {
			if err := later.AdoptTab(ctx, ref); err == nil {
				t.Errorf("%s: AdoptTab accepted %+v", name, ref)
			}
			if err := later.RunInTab(ctx, ref, "echo no"); err == nil {
				t.Errorf("%s: RunInTab accepted %+v after a refused adoption", name, ref)
			}
		}
		if sent := h.sent(); len(sent) != 0 {
			t.Errorf("refused adoptions reached the terminal: %q", sent)
		}
	})
	t.Run("adopt honours its context", func(t *testing.T) {
		h := newHost(t)
		ref := mustCreate(t, h.host, spec)
		done, cancel := context.WithCancel(ctx)
		cancel()
		if err := h.reopen().AdoptTab(done, ref); !errors.Is(err, context.Canceled) {
			t.Errorf("AdoptTab = %v", err)
		}
	})
	t.Run("typing into a recorded tab reaches its running program", func(t *testing.T) {
		h := newHost(t)
		ref := mustCreate(t, h.host, spec)
		if err := h.host.RunInTab(ctx, ref, "sleep 60"); err != nil {
			t.Fatal(err)
		}
		if err := typeRecorded(ctx, h.reopen(), ref, wake); err != nil {
			t.Fatal(err)
		}
		if sent := h.sent(); len(sent) != 2 || sent[1] != "run "+ref.PaneID+" "+wake {
			t.Errorf("sent = %q", sent)
		}
	})
	t.Run("typing into a recorded tab whose shell is idle sends nothing", func(t *testing.T) {
		h := newHost(t)
		ref := mustCreate(t, h.host, spec)
		if err := typeRecorded(ctx, h.reopen(), ref, wake); !errors.Is(err, ErrShellIdle) {
			t.Errorf("typeRecorded = %v, want ErrShellIdle", err)
		}
		missing := ref
		missing.TabID, missing.PaneID = "w1:t99", "w1:p99"
		if err := typeRecorded(ctx, h.reopen(), missing, wake); err == nil {
			t.Error("typeRecorded reached a missing tab")
		}
		if sent := h.sent(); len(sent) != 0 {
			t.Errorf("sent = %q, want nothing", sent)
		}
	})
}

// testWorkspaces is the contract for the session's workspaces, which the
// Director's tab is opened in (R1.37).
func testWorkspaces(t *testing.T, newHost func(t *testing.T) tabHostUnderTest) {
	ctx := context.Background()
	count := func(t *testing.T, h TabHost, label string) int {
		t.Helper()
		list, err := h.Workspaces(ctx)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, w := range list {
			if w.Label == label {
				n++
			}
		}
		return n
	}

	t.Run("a created workspace is listed and accepts tabs", func(t *testing.T) {
		h := newHost(t)
		before, err := h.host.Workspaces(ctx)
		if err != nil {
			t.Fatal(err)
		}
		w, err := h.host.CreateWorkspace(ctx, "ada", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if w.ID == "" || w.Label != "ada" {
			t.Errorf("created workspace = %+v", w)
		}
		// Listing preserves workspace identity, not creation-only tab ownership.
		w.InitialTab = herdr.OwnedTab{}
		after, err := h.host.Workspaces(ctx)
		if err != nil || len(after) != len(before)+1 || !slices.Contains(after, w) {
			t.Errorf("workspaces = %+v, %v; want %+v and %d before it", after, err, w, len(before))
		}
		if !slices.Contains(mustWorkspaces(t, h.reopen()), w) {
			t.Error("a later host on the terminal does not list the workspace")
		}
		ref, err := h.host.CreateTab(ctx, herdr.TabSpec{WorkspaceID: w.ID, Label: "director ada", Cwd: t.TempDir()})
		if err != nil || ref.WorkspaceID != w.ID {
			t.Errorf("tab in the new workspace = %+v, %v", ref, err)
		}
		for _, sent := range h.sent() {
			if sent != "close "+w.ID+":t1" {
				t.Errorf("workspace operations typed into a terminal: %q", sent)
			}
		}
	})
	t.Run("ensure creates a workspace once and reuses it by label", func(t *testing.T) {
		h := newHost(t)
		first, err := EnsureWorkspace(ctx, h.host, "ada", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		again, err := EnsureWorkspace(ctx, h.reopen(), "ada", t.TempDir())
		if err != nil || again != first {
			t.Errorf("second ensure = %q, %v; want %q", again, err, first)
		}
		if n := count(t, h.host, "ada"); n != 1 {
			t.Errorf("%d workspaces labelled ada, want 1", n)
		}
		other, err := EnsureWorkspace(ctx, h.host, "grace", t.TempDir())
		if err != nil || other == first || count(t, h.host, "grace") != 1 {
			t.Errorf("ensure grace = %q, %v; want a second workspace", other, err)
		}
	})
	t.Run("an ended context fails the workspace operations", func(t *testing.T) {
		h := newHost(t)
		done, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := h.host.Workspaces(done); !errors.Is(err, context.Canceled) {
			t.Errorf("Workspaces = %v", err)
		}
		if _, err := h.host.CreateWorkspace(done, "ada", ""); !errors.Is(err, context.Canceled) {
			t.Errorf("CreateWorkspace = %v", err)
		}
		if _, err := EnsureWorkspace(done, h.host, "ada", ""); !errors.Is(err, context.Canceled) {
			t.Errorf("EnsureWorkspace = %v", err)
		}
		if n := count(t, h.host, "ada"); n != 0 {
			t.Errorf("cancelled calls created %d workspaces", n)
		}
	})
}

func mustWorkspaces(t *testing.T, h TabHost) []herdr.Workspace {
	t.Helper()
	list, err := h.Workspaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func mustCreate(t *testing.T, h TabHost, spec herdr.TabSpec) TabRef {
	t.Helper()
	ref, err := h.CreateTab(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestHerdrTabsContract(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	newHost := func(t *testing.T) tabHostUnderTest {
		dir := t.TempDir()
		t.Setenv(tabsStubEnv, dir)
		client := func(session string) *HerdrTabs {
			c, err := herdr.New(herdr.Options{Session: session, Command: []string{exe}})
			if err != nil {
				t.Fatal(err)
			}
			return NewHerdrTabs(c)
		}
		return tabHostUnderTest{
			host:    client("lab"),
			foreign: client("other-lab"),
			session: "lab",
			sent: func() []string {
				return readLines(t, filepath.Join(dir, "sent-lab"))
			},
			reopen: func() TabHost { return client("lab") },
		}
	}
	testTabHost(t, newHost)
	testRecordedTabs(t, newHost)
	testWorkspaces(t, newHost)
}

func TestFakeTabsContract(t *testing.T) {
	newHost := func(t *testing.T) tabHostUnderTest {
		f := newFakeTabs("lab", "bash")
		return tabHostUnderTest{host: f, foreign: newFakeTabs("other-lab", "bash"), session: "lab", sent: f.sentLines,
			reopen: func() TabHost { return f.reopen() }}
	}
	testTabHost(t, newHost)
	testRecordedTabs(t, newHost)
	testWorkspaces(t, newHost)
}

// tabsStubEnv makes the test binary act as the Herdr executable, keeping
// workspace w1, labelled lab, the workspaces it creates, their tabs and pane
// states as files in the named directory.
const tabsStubEnv = "GUILDHALL_ADAPTER_TABS_STUB"

// stubHerdr answers the workspace, tab and pane commands HerdrTabs sends, in the shapes
// recorded from Herdr 0.9.5, and logs the input that reached each pane.
func stubHerdr(dir string, args []string) int {
	n := len(args)
	if n < 4 || args[n-2] != "--session" {
		fmt.Fprintf(os.Stderr, "stub herdr: call without --session: %q\n", args)
		return 2
	}
	session, args := args[n-1], args[:n-2]
	fail := func(code, msg string) int {
		fmt.Fprintf(os.Stderr, `{"error":{"code":%q,"message":%q},"id":"stub"}`, code, msg)
		return 1
	}
	pane := func(id string) (string, bool) {
		p := filepath.Join(dir, session+"-pane-"+strings.ReplaceAll(id, ":", "_"))
		_, err := os.Stat(p)
		return p, err == nil
	}
	logSent := func(line string) {
		f, err := os.OpenFile(filepath.Join(dir, "sent-"+session), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err == nil {
			_, _ = fmt.Fprintln(f, line)
			_ = f.Close()
		}
	}
	tabFile := func(id string) string { return filepath.Join(dir, session+"-tab-"+strings.ReplaceAll(id, ":", "_")) }
	closedWorkspace := func(id string) string { return filepath.Join(dir, session+"-closed-"+id) }
	allWorkspaces := func() [][2]string {
		list := [][2]string{{"w1", "lab"}}
		b, _ := os.ReadFile(filepath.Join(dir, session+"-workspaces"))
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if id, label, ok := strings.Cut(line, "\t"); ok {
				list = append(list, [2]string{id, label})
			}
		}
		return list
	}
	workspaces := func() [][2]string {
		var open [][2]string
		for _, w := range allWorkspaces() {
			if _, err := os.Stat(closedWorkspace(w[0])); err != nil {
				open = append(open, w)
			}
		}
		return open
	}
	tabLabel := func(id string) string {
		if b, err := os.ReadFile(tabFile(id) + "-label"); err == nil {
			return string(b)
		}
		return "x"
	}
	paneCount := func(id string) int {
		if _, err := os.Stat(tabFile(id) + "-split"); err == nil {
			return 2
		}
		return 1
	}
	// tabsIn lists the tabs whose file records workspace ws.
	tabsIn := func(ws string) []string {
		entries, _ := os.ReadDir(dir)
		var ids []string
		for _, e := range entries {
			name, ok := strings.CutPrefix(e.Name(), session+"-tab-")
			if !ok || strings.Contains(name, "-") {
				continue
			}
			id := strings.ReplaceAll(name, "_", ":")
			if b, err := os.ReadFile(tabFile(id)); err == nil && string(b) == ws {
				ids = append(ids, id)
			}
		}
		return ids
	}
	flag := func(name string) string {
		if i := slices.Index(args, name); i >= 0 && i+1 < len(args) {
			return args[i+1]
		}
		return ""
	}
	switch strings.Join(args[:2], " ") {
	case "workspace list":
		var items []string
		for i, w := range workspaces() {
			items = append(items, fmt.Sprintf(`{"active_tab_id":"%[1]s:t1","agent_status":"unknown","focused":false,"label":%[2]q,"number":%[3]d,"pane_count":1,"tab_count":1,"workspace_id":%[1]q}`, w[0], w[1], i+1))
		}
		fmt.Printf(`{"id":"cli:workspace:list","result":{"type":"workspace_list","workspaces":[%s]}}`, strings.Join(items, ","))
		return 0
	case "workspace create":
		if !slices.Contains(args, "--no-focus") {
			fmt.Fprintf(os.Stderr, "stub herdr: workspace create without --no-focus: %q\n", args)
			return 2
		}
		id, label := fmt.Sprintf("w%d", len(allWorkspaces())+1), flag("--label")
		f, err := os.OpenFile(filepath.Join(dir, session+"-workspaces"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return 2
		}
		_, err = fmt.Fprintf(f, "%s\t%s\n", id, label)
		if errors.Join(err, f.Close()) != nil {
			return 2
		}
		p, _ := pane(id + ":p1")
		if os.WriteFile(p, []byte("idle"), 0o600) != nil || os.WriteFile(tabFile(id+":t1"), []byte(id), 0o600) != nil {
			return 2
		}
		fmt.Printf(`{"id":"cli:workspace:create","result":{"root_pane":{"agent_status":"unknown","focused":false,"pane_id":"%[1]s:p1","tab_id":"%[1]s:t1","workspace_id":%[1]q},"tab":{"agent_status":"unknown","focused":false,"label":"1","number":1,"pane_count":1,"tab_id":"%[1]s:t1","workspace_id":%[1]q},"type":"workspace_created","workspace":{"active_tab_id":"%[1]s:t1","agent_status":"unknown","focused":false,"label":%[2]q,"number":%[3]d,"pane_count":1,"tab_count":1,"workspace_id":%[1]q}}}`, id, label, len(workspaces()))
		return 0
	case "tab create":
		ws := flag("--workspace")
		if !slices.ContainsFunc(workspaces(), func(w [2]string) bool { return w[0] == ws }) {
			return fail("workspace_not_found", "workspace not found")
		}
		counter := filepath.Join(dir, session+"-tabs")
		b, _ := os.ReadFile(counter)
		k, _ := strconv.Atoi(string(b))
		k++
		if err := os.WriteFile(counter, []byte(strconv.Itoa(k)), 0o600); err != nil {
			return 2
		}
		tab, paneID := fmt.Sprintf("%s:t%d", ws, k+1), fmt.Sprintf("%s:p%d", ws, k+1)
		p, _ := pane(paneID)
		if os.WriteFile(p, []byte("idle"), 0o600) != nil || os.WriteFile(tabFile(tab), []byte(ws), 0o600) != nil ||
			os.WriteFile(tabFile(tab)+"-label", []byte(flag("--label")), 0o600) != nil {
			return 2
		}
		fmt.Printf(`{"id":"cli:tab:create","result":{"root_pane":{"pane_id":%[2]q,"tab_id":%[1]q,"workspace_id":%[3]q},"tab":{"label":%[4]q,"tab_id":%[1]q,"workspace_id":%[3]q,"pane_count":1},"type":"tab_created"}}`, tab, paneID, ws, flag("--label"))
		return 0
	case "tab list":
		ws := flag("--workspace")
		if !slices.ContainsFunc(workspaces(), func(w [2]string) bool { return w[0] == ws }) {
			return fail("workspace_not_found", "workspace not found")
		}
		var items []string
		for _, id := range tabsIn(ws) {
			items = append(items, fmt.Sprintf(`{"label":%q,"pane_count":%d,"tab_id":%q,"workspace_id":%q}`, tabLabel(id), paneCount(id), id, ws))
		}
		fmt.Printf(`{"id":"cli:tab:list","result":{"tabs":[%s],"type":"tab_list"}}`, strings.Join(items, ","))
		return 0
	case "workspace close":
		ws := args[2]
		if !slices.ContainsFunc(workspaces(), func(w [2]string) bool { return w[0] == ws }) {
			return fail("workspace_not_found", "workspace not found")
		}
		for _, id := range tabsIn(ws) {
			_ = os.Remove(tabFile(id))
		}
		if os.WriteFile(closedWorkspace(ws), nil, 0o600) != nil {
			return 2
		}
		logSent("close-workspace " + ws)
		fmt.Print(`{"id":"cli:workspace:close","result":{"type":"ok"}}`)
		return 0
	case "tab close":
		if err := os.Remove(tabFile(args[2])); err != nil {
			return fail("tab_not_found", "tab not found")
		}
		logSent("close " + args[2])
		fmt.Print(`{"id":"cli:tab:close","result":{"type":"tab_closed"}}`)
		return 0
	case "tab get":
		ws, err := os.ReadFile(tabFile(args[2]))
		if err != nil {
			return fail("tab_not_found", "tab "+args[2]+" not found")
		}
		fmt.Printf(`{"id":"cli:tab:get","result":{"tab":{"agent_status":"unknown","focused":false,"label":%q,"number":2,"pane_count":%d,"tab_id":%q,"workspace_id":%q},"type":"tab_info"}}`, tabLabel(args[2]), paneCount(args[2]), args[2], ws)
		return 0
	case "pane get":
		if _, ok := pane(args[2]); !ok {
			return fail("pane_not_found", "pane not found")
		}
		ws, suffix, _ := strings.Cut(args[2], ":p")
		tabID := ws + ":t" + suffix
		if _, err := os.Stat(tabFile(tabID) + "-moved-pane"); err == nil {
			tabID = ws + ":t999"
		}
		fmt.Printf(`{"id":"cli:pane:get","result":{"pane":{"pane_id":%q,"tab_id":%q,"workspace_id":%q},"type":"pane_info"}}`, args[2], tabID, ws)
		return 0
	case "pane process-info":
		p, ok := pane(args[3])
		if !ok {
			return fail("pane_not_found", "pane not found")
		}
		// A one-shot change trigger closes the pane's tab, as another client
		// could while a close checks the workspace's tabs.
		ws, suffix, _ := strings.Cut(args[3], ":p")
		if trigger := filepath.Join(dir, session+"-change-"+ws); os.Remove(trigger) == nil {
			if os.Remove(tabFile(ws+":t"+suffix)) != nil {
				return 2
			}
		}
		state, _ := os.ReadFile(p)
		fg := `{"pid":10,"name":"bash","argv":["bash"]}`
		if string(state) == "busy" || string(state) == "startup" {
			fg = `{"pid":11,"name":"sleep","argv":["sleep","60"]}`
		}
		if string(state) == "startup" {
			if err := os.WriteFile(p, []byte("idle"), 0o600); err != nil {
				return 2
			}
		}
		fmt.Printf(`{"id":"cli:pane:process_info","result":{"process_info":{"pane_id":%q,"shell_pid":10,"foreground_processes":[%s]},"type":"pane_process_info"}}`, args[3], fg)
		return 0
	case "pane run":
		p, ok := pane(args[2])
		if !ok {
			return fail("pane_not_found", "pane not found")
		}
		logSent("run " + args[2] + " " + args[3])
		return boolExit(os.WriteFile(p, []byte("busy"), 0o600) == nil)
	case "pane send-keys":
		p, ok := pane(args[2])
		if !ok {
			return fail("pane_not_found", "pane not found")
		}
		logSent("keys " + args[2] + " " + args[3])
		return boolExit(os.WriteFile(p, []byte("idle"), 0o600) == nil)
	}
	fmt.Fprintf(os.Stderr, "stub herdr: unexpected call %q\n", args)
	return 2
}

func boolExit(ok bool) int {
	if ok {
		return 0
	}
	return 2
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

// fakeTabs is the TabHost double for launcher tests: workspace w1, labelled
// lab, then any it creates, tabs numbered per terminal, a shell that is busy for busyReads reads after
// creation, and input logged per pane. Hosts from reopen share the terminal
// and own only the tabs they create or adopt.
type fakeTabs struct {
	*fakeTerminal
	owned []TabRef
}

// fakeTerminal is the state hosts on one fake terminal share, under mu.
type fakeTerminal struct {
	session   string
	shell     string
	busyReads int
	runErr    error

	mu         sync.Mutex
	workspaces []herdr.Workspace
	next       int
	created    []herdr.TabSpec
	tabs       []TabRef
	busy       map[string]bool
	sent       []string
}

func newFakeTabs(session, shell string) *fakeTabs {
	return &fakeTabs{fakeTerminal: &fakeTerminal{session: session, shell: shell, busy: map[string]bool{},
		workspaces: []herdr.Workspace{{ID: "w1", Label: "lab"}}}}
}

// reopen returns a new host on f's terminal that owns no tab.
func (f *fakeTabs) reopen() *fakeTabs { return &fakeTabs{fakeTerminal: f.fakeTerminal} }

func (f *fakeTabs) Workspaces(ctx context.Context) ([]herdr.Workspace, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.workspaces), nil
}

func (f *fakeTabs) CreateWorkspace(ctx context.Context, label, cwd string) (herdr.Workspace, error) {
	if err := ctx.Err(); err != nil {
		return herdr.Workspace{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w := herdr.Workspace{ID: fmt.Sprintf("w%d", len(f.workspaces)+1), Label: label}
	f.workspaces = append(f.workspaces, w)
	return w, nil
}

func (f *fakeTabs) CreateTab(ctx context.Context, spec herdr.TabSpec) (TabRef, error) {
	if err := ctx.Err(); err != nil {
		return TabRef{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.ContainsFunc(f.workspaces, func(w herdr.Workspace) bool { return w.ID == spec.WorkspaceID }) {
		return TabRef{}, &herdr.APIError{Code: "workspace_not_found", Message: "workspace not found"}
	}
	f.created = append(f.created, spec)
	f.next++
	ref := TabRef{Session: f.session, WorkspaceID: spec.WorkspaceID, TabID: fmt.Sprintf("%s:t%d", spec.WorkspaceID, f.next+1),
		PaneID: fmt.Sprintf("%s:p%d", spec.WorkspaceID, f.next+1)}
	f.tabs = append(f.tabs, ref)
	f.owned = append(f.owned, ref)
	return ref, nil
}

func (f *fakeTabs) AdoptTab(ctx context.Context, ref TabRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Contains(f.tabs, ref) {
		return &herdr.APIError{Code: "tab_not_found", Message: "tab " + ref.TabID + " not found"}
	}
	f.owned = append(f.owned, ref)
	return nil
}

// check refuses a tab this host did not create, under the lock.
func (f *fakeTabs) check(ctx context.Context, ref TabRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !slices.Contains(f.owned, ref) {
		return fmt.Errorf("tab %q in session %q: %w", ref.TabID, ref.Session, errNotOwned)
	}
	return nil
}

func (f *fakeTabs) PaneProcessInfo(ctx context.Context, ref TabRef) (herdr.ProcessInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(ctx, ref); err != nil {
		return herdr.ProcessInfo{}, err
	}
	if f.busyReads > 0 || f.busy[ref.PaneID] {
		if f.busyReads > 0 {
			f.busyReads--
		}
		return herdr.ProcessInfo{ShellPID: 10, Foreground: []herdr.Process{{PID: 11, Name: "sleep"}}}, nil
	}
	return herdr.ProcessInfo{ShellPID: 10, Foreground: []herdr.Process{{PID: 10, Name: f.shell}}}, nil
}

func (f *fakeTabs) RunInTab(ctx context.Context, ref TabRef, line string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(ctx, ref); err != nil {
		return err
	}
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return errors.New("herdr pane run needs exactly one non-empty line")
	}
	f.sent = append(f.sent, "run "+ref.PaneID+" "+line)
	f.busy[ref.PaneID] = true
	return f.runErr
}

func (f *fakeTabs) InterruptTab(ctx context.Context, ref TabRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(ctx, ref); err != nil {
		return err
	}
	f.sent = append(f.sent, "keys "+ref.PaneID+" ctrl+c")
	f.busy[ref.PaneID] = false
	return nil
}

func (f *fakeTabs) sentLines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}
