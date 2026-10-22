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

// tabHost is the terminal boundary TabLauncher consumes. testTabHost is its
// contract; it runs against HerdrTabs over a real herdr.Client whose Herdr
// executable is a stub (the test binary), and against the fakeTabs double the
// launcher tests use, so a launcher test that passes on the double means the
// same against the real client.

type tabHostUnderTest struct {
	host tabHost
	// foreign is another host of the same kind; its tabs must be refused.
	foreign tabHost
	session string
	// sent returns the lines and keys that reached the terminal, in order.
	sent func() []string
	// reopen returns a new host on the same terminal, as a later process would
	// make: it holds the tabs host created but owns none of them.
	reopen func() tabHost
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

func mustCreate(t *testing.T, h tabHost, spec herdr.TabSpec) TabRef {
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
			reopen: func() tabHost { return client("lab") },
		}
	}
	testTabHost(t, newHost)
	testRecordedTabs(t, newHost)
}

func TestFakeTabsContract(t *testing.T) {
	newHost := func(t *testing.T) tabHostUnderTest {
		f := newFakeTabs("lab", "bash")
		return tabHostUnderTest{host: f, foreign: newFakeTabs("other-lab", "bash"), session: "lab", sent: f.sentLines,
			reopen: func() tabHost { return f.reopen() }}
	}
	testTabHost(t, newHost)
	testRecordedTabs(t, newHost)
}

// tabsStubEnv makes the test binary act as the Herdr executable, keeping one
// workspace w1, its tabs and pane states as files in the named directory.
const tabsStubEnv = "GUILDHALL_ADAPTER_TABS_STUB"

// stubHerdr answers the tab and pane commands HerdrTabs sends, in the shapes
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
	switch strings.Join(args[:2], " ") {
	case "tab create":
		if i := slices.Index(args, "--workspace"); i < 0 || args[i+1] != "w1" {
			return fail("workspace_not_found", "workspace not found")
		}
		counter := filepath.Join(dir, session+"-tabs")
		b, _ := os.ReadFile(counter)
		k, _ := strconv.Atoi(string(b))
		k++
		if err := os.WriteFile(counter, []byte(strconv.Itoa(k)), 0o600); err != nil {
			return 2
		}
		p, _ := pane(fmt.Sprintf("w1:p%d", k+1))
		if err := os.WriteFile(p, []byte("idle"), 0o600); err != nil {
			return 2
		}
		fmt.Printf(`{"id":"cli:tab:create","result":{"root_pane":{"pane_id":"w1:p%[1]d","tab_id":"w1:t%[1]d","workspace_id":"w1"},"tab":{"label":"x","tab_id":"w1:t%[1]d","workspace_id":"w1","pane_count":1},"type":"tab_created"}}`, k+1)
		return 0
	case "tab get":
		counter, _ := os.ReadFile(filepath.Join(dir, session+"-tabs"))
		k, _ := strconv.Atoi(string(counter))
		var n int
		if _, err := fmt.Sscanf(args[2], "w1:t%d", &n); err != nil || n < 2 || n > k+1 {
			return fail("tab_not_found", "tab "+args[2]+" not found")
		}
		fmt.Printf(`{"id":"cli:tab:get","result":{"tab":{"agent_status":"unknown","focused":false,"label":"x","number":%d,"pane_count":1,"tab_id":%q,"workspace_id":"w1"},"type":"tab_info"}}`, n, args[2])
		return 0
	case "pane process-info":
		p, ok := pane(args[3])
		if !ok {
			return fail("pane_not_found", "pane not found")
		}
		state, _ := os.ReadFile(p)
		fg := `{"pid":10,"name":"bash","argv":["bash"]}`
		if string(state) == "busy" {
			fg = `{"pid":11,"name":"sleep","argv":["sleep","60"]}`
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

// fakeTabs is the tabHost double for launcher tests: one workspace w1, tabs
// numbered per terminal, a shell that is busy for busyReads reads after
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

	mu      sync.Mutex
	next    int
	created []herdr.TabSpec
	tabs    []TabRef
	busy    map[string]bool
	sent    []string
}

func newFakeTabs(session, shell string) *fakeTabs {
	return &fakeTabs{fakeTerminal: &fakeTerminal{session: session, shell: shell, busy: map[string]bool{}}}
}

// reopen returns a new host on f's terminal that owns no tab.
func (f *fakeTabs) reopen() *fakeTabs { return &fakeTabs{fakeTerminal: f.fakeTerminal} }

func (f *fakeTabs) CreateTab(ctx context.Context, spec herdr.TabSpec) (TabRef, error) {
	if err := ctx.Err(); err != nil {
		return TabRef{}, err
	}
	if spec.WorkspaceID != "w1" {
		return TabRef{}, &herdr.APIError{Code: "workspace_not_found", Message: "workspace not found"}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, spec)
	f.next++
	ref := TabRef{Session: f.session, WorkspaceID: spec.WorkspaceID, TabID: fmt.Sprintf("w1:t%d", f.next+1), PaneID: fmt.Sprintf("w1:p%d", f.next+1)}
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
