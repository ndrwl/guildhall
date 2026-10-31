package adapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"sync"
	"time"

	"github.com/ndrwl/guildhall/internal/herdr"
)

// Process is an Artisan run as a direct child process, the launch path tests
// and CI use without Herdr.
type Process struct {
	cmd    *exec.Cmd
	exited chan struct{}
	once   sync.Once
}

// StartProcess starts cmd as a child process with the current environment plus
// cmd.Env, writing its output to stdout and stderr (discarded when nil).
func StartProcess(cmd Command, stdout, stderr io.Writer) (*Process, error) {
	if len(cmd.Argv) == 0 || cmd.Argv[0] == "" {
		return nil, errors.New("artisan command is empty")
	}
	c := exec.Command(cmd.Argv[0], cmd.Argv[1:]...)
	c.Dir = cmd.Dir
	c.Env = append(os.Environ(), cmd.Env...)
	c.Stdout, c.Stderr = stdout, stderr
	if err := c.Start(); err != nil {
		return nil, err
	}
	p := &Process{cmd: c, exited: make(chan struct{})}
	go func() {
		// The exit status is not used: completion comes only from the result file.
		_ = c.Wait()
		close(p.exited)
	}()
	return p, nil
}

// Exited is closed once the process has exited.
func (p *Process) Exited() <-chan struct{} { return p.exited }

// Stop kills the process. Stopping an exited process is not an error.
func (p *Process) Stop() error {
	var err error
	p.once.Do(func() {
		err = p.cmd.Process.Kill()
		if errors.Is(err, os.ErrProcessDone) {
			err = nil
		}
	})
	return err
}

// TabRef names a tab a TabHost created. The value carries no authority: the
// host that created the tab is the only one that accepts it, and a host refuses
// the zero TabRef, a TabRef from another host and one it never returned.
type TabRef struct {
	Session     string
	WorkspaceID string
	TabID       string
	PaneID      string
}

// TabHost is the terminal a TabLauncher drives: it creates tabs and operates
// only on tabs it created. RunInTab success means the terminal accepted the
// line, not that the command ran or finished; it types into whatever runs in
// the pane. InterruptTab sends Ctrl+C and proves nothing about what stopped.
// AdoptTab accepts a tab an earlier host on the same terminal created, as a
// durable Guildhall record names it, once it has checked that the tab is still
// there in the host's session and the recorded workspace; it types nothing.
// Workspaces lists the session's workspaces, in the terminal's order, and
// CreateWorkspace creates one without taking focus; a created workspace is
// listed from then on and accepts tabs. Listing workspaces grants no tab
// ownership: any host on the terminal lists and uses them.
// Every operation honours its context.
type TabHost interface {
	Workspaces(ctx context.Context) ([]herdr.Workspace, error)
	CreateWorkspace(ctx context.Context, label, cwd string) (herdr.Workspace, error)
	CreateTab(ctx context.Context, spec herdr.TabSpec) (TabRef, error)
	AdoptTab(ctx context.Context, tab TabRef) error
	PaneProcessInfo(ctx context.Context, tab TabRef) (herdr.ProcessInfo, error)
	RunInTab(ctx context.Context, tab TabRef, line string) error
	InterruptTab(ctx context.Context, tab TabRef) error
}

// errNotOwned reports a TabRef the host did not create.
var errNotOwned = errors.New("tab was not created by this host")

// HerdrTabs is the TabHost backed by a Herdr client. It keeps the client's
// ownership proof for created or durably adopted tabs and accepts only those.
type HerdrTabs struct {
	client   *herdr.Client
	mu       sync.Mutex
	owned    map[TabRef]herdr.OwnedTab
	starting map[string]TabRef
}

// NewHerdrTabs returns a TabHost for client.
func NewHerdrTabs(client *herdr.Client) *HerdrTabs {
	return &HerdrTabs{client: client, owned: map[TabRef]herdr.OwnedTab{}, starting: map[string]TabRef{}}
}

// Workspaces lists the session's workspaces.
func (h *HerdrTabs) Workspaces(ctx context.Context) ([]herdr.Workspace, error) {
	return h.client.WorkspaceList(ctx)
}

// CreateWorkspace creates a workspace without taking focus and captures its
// starting shell's identity, which the next CreateTab in it will replace.
func (h *HerdrTabs) CreateWorkspace(ctx context.Context, label, cwd string) (herdr.Workspace, error) {
	w, err := h.client.WorkspaceCreate(ctx, label, cwd)
	if err != nil {
		return w, err
	}
	t := w.InitialTab
	ref := TabRef{Session: t.Session(), WorkspaceID: t.WorkspaceID(), TabID: t.ID(), PaneID: t.PaneID()}
	h.mu.Lock()
	h.owned[ref] = t
	h.starting[w.ID] = ref
	h.mu.Unlock()
	return w, nil
}

// EnsureWorkspace returns the id of h's first workspace labelled label,
// creating one with cwd, without focus, when there is none (R1.37). Two
// callers racing can each create one; later calls reuse the first listed.
func EnsureWorkspace(ctx context.Context, h TabHost, label, cwd string) (string, error) {
	list, err := h.Workspaces(ctx)
	if err != nil {
		return "", err
	}
	for _, w := range list {
		if w.Label == label {
			return w.ID, nil
		}
	}
	w, err := h.CreateWorkspace(ctx, label, cwd)
	if err != nil {
		return "", err
	}
	return w.ID, nil
}

// CreateTab creates a tab without taking focus. In a workspace this host just
// created, it waits for the owned starting shell's prompt and closes that shell
// after the replacement exists. A cleanup failure returns the new tab and an
// error naming both tabs, leaving the shells open and typing nothing.
func (h *HerdrTabs) CreateTab(ctx context.Context, spec herdr.TabSpec) (TabRef, error) {
	t, err := h.client.TabCreate(ctx, spec)
	if err != nil {
		return TabRef{}, err
	}
	ref := TabRef{Session: t.Session(), WorkspaceID: t.WorkspaceID(), TabID: t.ID(), PaneID: t.PaneID()}
	h.mu.Lock()
	h.owned[ref] = t
	starter, ok := h.starting[spec.WorkspaceID]
	delete(h.starting, spec.WorkspaceID)
	h.mu.Unlock()
	if ok {
		// As for the replacement, allow the starting shell to finish startup.
		if _, err := (TabLauncher{Host: h}).waitShell(ctx, starter); err != nil {
			return ref, fmt.Errorf("replacement tab %s created, but starting shell %s left open: %w", ref.TabID, starter.TabID, err)
		}
		if err := h.CloseTab(ctx, starter); err != nil {
			return ref, fmt.Errorf("replacement tab %s created, but starting shell %s left open: %w", ref.TabID, starter.TabID, err)
		}
	}
	return ref, nil
}

func (h *HerdrTabs) lookup(ref TabRef) (herdr.OwnedTab, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.owned[ref]
	if !ok {
		return herdr.OwnedTab{}, fmt.Errorf("tab %q in session %q: %w", ref.TabID, ref.Session, errNotOwned)
	}
	return t, nil
}

// AdoptTab accepts a tab Guildhall created and recorded in an earlier process.
func (h *HerdrTabs) AdoptTab(ctx context.Context, ref TabRef) error {
	if ref.Session != h.client.Session() {
		return leftOpen("tab %q is in session %q, not %q", ref.TabID, ref.Session, h.client.Session())
	}
	t, err := h.client.RecordedTab(ctx, ref.WorkspaceID, ref.TabID, ref.PaneID)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.owned[ref] = t
	return nil
}

// TypeRecorded types one line into a recorded tab; see typeRecorded.
func (h *HerdrTabs) TypeRecorded(ctx context.Context, ref TabRef, line string) error {
	return typeRecorded(ctx, h, ref, line)
}

// ErrShellIdle reports a recorded pane whose shell is its only foreground
// process, so the program the record names is no longer running there and a
// typed line would reach the shell prompt.
var ErrShellIdle = errors.New("the pane's shell is idle; its program is not running")

// typeRecorded types one line and Enter into a tab an earlier Guildhall
// process created and recorded, such as the Director's pane: it adopts the
// tab, then types only while a program other than the shell runs in it, so a
// line meant for that program never reaches a shell prompt (ErrShellIdle).
// Success means the terminal accepted the line, not that the program read it.
func typeRecorded(ctx context.Context, h TabHost, ref TabRef, line string) error {
	if err := h.AdoptTab(ctx, ref); err != nil {
		return err
	}
	info, err := h.PaneProcessInfo(ctx, ref)
	if err != nil {
		return err
	}
	if info.ShellIdle() {
		return ErrShellIdle
	}
	return h.RunInTab(ctx, ref, line)
}

// PaneProcessInfo reads the shell and foreground processes of a created tab.
func (h *HerdrTabs) PaneProcessInfo(ctx context.Context, ref TabRef) (herdr.ProcessInfo, error) {
	t, err := h.lookup(ref)
	if err != nil {
		return herdr.ProcessInfo{}, err
	}
	return h.client.PaneProcessInfo(ctx, t.PaneID())
}

// RunInTab types one line and Enter into a created tab.
func (h *HerdrTabs) RunInTab(ctx context.Context, ref TabRef, line string) error {
	t, err := h.lookup(ref)
	if err != nil {
		return err
	}
	return h.client.RunInOwnedTab(ctx, t, line)
}

// InterruptTab sends Ctrl+C to a created tab.
func (h *HerdrTabs) InterruptTab(ctx context.Context, ref TabRef) error {
	t, err := h.lookup(ref)
	if err != nil {
		return err
	}
	return h.client.InterruptOwnedTab(ctx, t)
}

// IdleTabCloser is the presentation-only terminal cleanup boundary.
// CloseTab accepts only a created or durably adopted identity, revalidates it,
// and refuses a pane whose shell is not idle. Callers must separately prove
// that any recorded Director session is stopped before invoking it.
// Closing never updates a task, attempt or session record.
type IdleTabCloser interface {
	CloseTab(context.Context, TabRef) error
}

// CloseTab closes an owned idle shell, never an arbitrary tab selected by label.
func (h *HerdrTabs) CloseTab(ctx context.Context, ref TabRef) error {
	return h.closeTab(ctx, ref, "")
}

// CloseLabelledTab is CloseTab that also requires the tab to be labelled label.
func (h *HerdrTabs) CloseLabelledTab(ctx context.Context, ref TabRef, label string) error {
	if label == "" {
		return errors.New("close tab: no label to match")
	}
	return h.closeTab(ctx, ref, label)
}

// closeTab revalidates ref, and its label unless label is empty, and closes it.
func (h *HerdrTabs) closeTab(ctx context.Context, ref TabRef, label string) error {
	if _, err := h.lookup(ref); err != nil {
		return err
	}
	t, err := h.client.RecordedTab(ctx, ref.WorkspaceID, ref.TabID, ref.PaneID)
	if err != nil {
		return err
	}
	tab, err := h.client.TabGet(ctx, ref.TabID)
	if err != nil {
		return err
	}
	if tab.ID != ref.TabID || tab.PaneCount != 1 {
		return leftOpen("tab identity changed or tab has multiple panes; tab left open")
	}
	if label != "" && tab.Label != label {
		return leftOpen("tab %s is labelled %q, not %q; tab left open", ref.TabID, tab.Label, label)
	}
	if err := h.client.CheckOwnedPane(ctx, t); err != nil {
		return err
	}
	info, err := h.client.PaneProcessInfo(ctx, ref.PaneID)
	if err != nil {
		return err
	}
	if !info.ShellIdle() {
		return leftOpen("pane is not an idle shell; tab left open")
	}
	if err := h.client.CloseOwnedTab(ctx, t); err != nil {
		return err
	}
	h.mu.Lock()
	delete(h.owned, ref)
	h.mu.Unlock()
	return nil
}

// LeftOpenError is a close that revalidation refused because the live terminal
// no longer matches the durable record: Reason says what differs. Retrying
// later may succeed once the terminal matches again.
type LeftOpenError struct {
	Reason string
}

func (e *LeftOpenError) Error() string { return e.Reason }

func leftOpen(format string, args ...any) error {
	return &LeftOpenError{Reason: fmt.Sprintf(format, args...)}
}

// IsLeftOpen reports whether err is a close refused by revalidation, a
// LeftOpenError or a recorded tab or pane that moved (herdr.ErrMoved), rather
// than a terminal that could not be asked.
func IsLeftOpen(err error) bool {
	var l *LeftOpenError
	return errors.As(err, &l) || errors.Is(err, herdr.ErrMoved)
}

// IdleWorkspaceCloser closes the Herdr workspace a finished Artisan attempt
// was presented in, as its durable record names it. CloseWorkspace accepts
// only a created or durably adopted tab identity and revalidates the whole
// workspace immediately before closing: ref's session is the host's, the
// workspace exists and is labelled one of workspaceLabels, it holds exactly
// one tab, ref's, labelled tabLabel with one pane, the pane is still ref's and
// its shell is idle. A mismatch is a LeftOpenError; a workspace, tab or pane
// already gone is an error for which herdr.IsNotFound is true.
// CloseLabelledTab closes only ref's tab, as IdleTabCloser does, once its
// label is tabLabel. Callers must separately prove that the attempt finished
// and its launcher is gone. Closing never updates a task, attempt or session
// record.
type IdleWorkspaceCloser interface {
	CloseWorkspace(ctx context.Context, ref TabRef, workspaceLabels []string, tabLabel string) error
	CloseLabelledTab(ctx context.Context, ref TabRef, tabLabel string) error
}

var _ IdleWorkspaceCloser = (*HerdrTabs)(nil)

// workspaceNotFound is the not-found error of a workspace no longer listed.
func workspaceNotFound(id string) error {
	return &herdr.APIError{Code: "workspace_not_found", Message: fmt.Sprintf("workspace %s is not listed", id)}
}

// CloseWorkspace closes ref's workspace once it holds nothing but ref's idle tab.
func (h *HerdrTabs) CloseWorkspace(ctx context.Context, ref TabRef, workspaceLabels []string, tabLabel string) error {
	if ref.Session != h.client.Session() {
		return leftOpen("workspace %s is recorded in session %q, not %q", ref.WorkspaceID, ref.Session, h.client.Session())
	}
	if _, err := h.lookup(ref); err != nil {
		return err
	}
	if len(workspaceLabels) == 0 || tabLabel == "" {
		return errors.New("close workspace: no labels to match")
	}
	list, err := h.client.WorkspaceList(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(list, func(w herdr.Workspace) bool { return w.ID == ref.WorkspaceID })
	if i < 0 {
		return workspaceNotFound(ref.WorkspaceID)
	}
	if label := list[i].Label; !slices.Contains(workspaceLabels, label) {
		return leftOpen("workspace %s is labelled %q, not an attempt's workspace label", ref.WorkspaceID, label)
	}
	tabs, err := h.client.TabList(ctx, ref.WorkspaceID)
	if err != nil {
		return err
	}
	switch {
	case len(tabs) != 1:
		return leftOpen("workspace %s holds %d tabs, not only the recorded tab %s", ref.WorkspaceID, len(tabs), ref.TabID)
	case tabs[0].ID != ref.TabID:
		return leftOpen("workspace %s holds tab %s, not the recorded tab %s", ref.WorkspaceID, tabs[0].ID, ref.TabID)
	case tabs[0].Label != tabLabel:
		return leftOpen("tab %s is labelled %q, not %q", ref.TabID, tabs[0].Label, tabLabel)
	case tabs[0].PaneCount != 1:
		return leftOpen("tab %s holds %d panes, not one", ref.TabID, tabs[0].PaneCount)
	}
	t, err := h.client.RecordedTab(ctx, ref.WorkspaceID, ref.TabID, ref.PaneID)
	if err != nil {
		return err
	}
	if err := h.client.CheckOwnedPane(ctx, t); err != nil {
		return err
	}
	info, err := h.client.PaneProcessInfo(ctx, ref.PaneID)
	if err != nil {
		return err
	}
	if !info.ShellIdle() {
		return leftOpen("pane %s is not an idle shell", ref.PaneID)
	}
	if err := h.client.CloseOwnedWorkspace(ctx, t, ref.WorkspaceID); err != nil {
		return err
	}
	h.mu.Lock()
	delete(h.owned, ref)
	h.mu.Unlock()
	return nil
}

// AbandonedWorkspaceCloser closes the Herdr workspace a failed Artisan launch
// created and abandoned before it typed anything, as the launch's launching
// claim records it: workspaceID and tabs, the tabs it created there in
// creation order. On a host whose new workspaces hold a starting tab of their
// own, tabs[0] is that starting tab, whose label the terminal chooses; every
// other recorded tab must be labelled tabLabel.
//
// CloseAbandonedWorkspace does nothing and succeeds when the workspace is no
// longer listed. Otherwise it refuses, with a LeftOpenError, a recorded tab in
// another session or workspace, a workspace not labelled one of
// workspaceLabels, any listed tab that is not a recorded tab, a recorded tab
// other than the starting tab not labelled tabLabel, a tab holding anything
// but one pane, a pane no longer the recorded tab's, and a pane whose shell is
// not idle. It adopts each recorded tab still listed, revalidates, and closes
// the workspace through one of them; it never closes by label alone. Callers
// must separately prove that nothing was typed into the workspace. Closing
// never updates a task, attempt or session record.
type AbandonedWorkspaceCloser interface {
	CloseAbandonedWorkspace(ctx context.Context, workspaceID string, tabs []TabRef, workspaceLabels []string, tabLabel string) error
}

var _ AbandonedWorkspaceCloser = (*HerdrTabs)(nil)

// CloseAbandonedWorkspace closes a failed launch's workspace once it holds
// nothing but that launch's recorded idle tabs; see AbandonedWorkspaceCloser.
func (h *HerdrTabs) CloseAbandonedWorkspace(ctx context.Context, workspaceID string, tabs []TabRef, workspaceLabels []string, tabLabel string) error {
	if len(workspaceLabels) == 0 || tabLabel == "" {
		return errors.New("close abandoned workspace: no labels to match")
	}
	recorded := map[string]TabRef{}
	for _, ref := range tabs {
		if ref.Session != h.client.Session() {
			return leftOpen("workspace %s is recorded in session %q, not %q", workspaceID, ref.Session, h.client.Session())
		}
		if ref.WorkspaceID != workspaceID {
			return leftOpen("recorded tab %s is in workspace %s, not %s", ref.TabID, ref.WorkspaceID, workspaceID)
		}
		recorded[ref.TabID] = ref
	}
	list, err := h.client.WorkspaceList(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(list, func(w herdr.Workspace) bool { return w.ID == workspaceID })
	if i < 0 {
		return nil
	}
	if label := list[i].Label; !slices.Contains(workspaceLabels, label) {
		return leftOpen("workspace %s is labelled %q, not an attempt's workspace label", workspaceID, label)
	}
	listed, err := h.abandonedTabs(ctx, workspaceID, tabs, recorded, tabLabel)
	if err != nil {
		return err
	}
	owned := make([]herdr.OwnedTab, 0, len(listed))
	for _, ref := range listed {
		if err := h.AdoptTab(ctx, ref); err != nil {
			return err
		}
		t, err := h.lookup(ref)
		if err != nil {
			return err
		}
		if err := h.client.CheckOwnedPane(ctx, t); err != nil {
			return err
		}
		info, err := h.client.PaneProcessInfo(ctx, ref.PaneID)
		if err != nil {
			return err
		}
		if !info.ShellIdle() {
			return leftOpen("pane %s is not an idle shell", ref.PaneID)
		}
		owned = append(owned, t)
	}
	// Revalidate the workspace's tabs: nothing may have joined them meanwhile.
	again, err := h.abandonedTabs(ctx, workspaceID, tabs, recorded, tabLabel)
	if err != nil {
		return err
	}
	if !slices.Equal(again, listed) {
		return leftOpen("workspace %s changed its tabs while it was checked", workspaceID)
	}
	if err := h.client.CloseOwnedWorkspace(ctx, owned[0], workspaceID); err != nil {
		return err
	}
	h.mu.Lock()
	for _, ref := range tabs {
		delete(h.owned, ref)
	}
	delete(h.starting, workspaceID)
	h.mu.Unlock()
	return nil
}

// abandonedTabs lists workspaceID's tabs and returns their recorded refs, in
// listed order, once each is a recorded tab with one pane, labelled tabLabel
// unless it is the starting tab, tabs[0]; it refuses a workspace with no tab.
func (h *HerdrTabs) abandonedTabs(ctx context.Context, workspaceID string, tabs []TabRef, recorded map[string]TabRef, tabLabel string) ([]TabRef, error) {
	list, err := h.client.TabList(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, leftOpen("workspace %s holds no recorded tab to close it through", workspaceID)
	}
	refs := make([]TabRef, 0, len(list))
	for _, tab := range list {
		ref, ok := recorded[tab.ID]
		switch {
		case !ok:
			return nil, leftOpen("workspace %s holds tab %s, which the launch did not record", workspaceID, tab.ID)
		case tab.PaneCount != 1:
			return nil, leftOpen("tab %s holds %d panes, not one", tab.ID, tab.PaneCount)
		case ref != tabs[0] && tab.Label != tabLabel:
			return nil, leftOpen("tab %s is labelled %q, not %q", tab.ID, tab.Label, tabLabel)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// TabLauncher starts an Artisan in a new, visible Herdr tab (R1.38).
type TabLauncher struct {
	Host        TabHost
	WorkspaceID string
	// ShellReady bounds the wait for the new tab's shell to reach its prompt.
	ShellReady time.Duration
	// Poll is the interval between shell readiness checks.
	Poll time.Duration
}

// TabRun is an Artisan started in a Herdr tab. The tab is a presentation
// binding only (R1.52): its closing or disappearance never completes the run.
type TabRun struct {
	host TabHost
	Tab  TabRef
}

// Start creates a tab labelled label in the launcher's workspace, waits until
// its shell is the only foreground process, and types cmd into it, quoted for
// that shell. A tab whose shell is unsupported or never becomes idle is left
// open with nothing typed into it.
func (l TabLauncher) Start(ctx context.Context, label string, cmd Command) (*TabRun, error) {
	if len(cmd.Argv) == 0 {
		return nil, errors.New("artisan command is empty")
	}
	tab, shell, err := l.Open(ctx, label, cmd.Dir, cmd.Env)
	if err != nil {
		return nil, err
	}
	if err := l.Type(ctx, tab, shell, cmd.Argv); err != nil {
		return nil, err
	}
	return &TabRun{host: l.Host, Tab: tab}, nil
}

// Open creates a tab labelled label in the launcher's workspace, with working
// directory dir and env added to its shell's environment, and waits until its
// shell is the only foreground process, returning the tab and the shell's
// process name. A tab whose shell never becomes idle is left open. An error
// that follows the tab's creation, such as the host's report that it created
// the tab but left the workspace's starting shell open, or the shell not
// becoming idle, comes with the created tab, so the caller can record it; any
// other error comes with the zero TabRef.
func (l TabLauncher) Open(ctx context.Context, label, dir string, env []string) (TabRef, string, error) {
	tab, err := l.Host.CreateTab(ctx, herdr.TabSpec{WorkspaceID: l.WorkspaceID, Label: label, Cwd: dir, Env: env})
	if err != nil {
		return tab, "", err
	}
	shell, err := l.waitShell(ctx, tab)
	if err != nil {
		return tab, "", fmt.Errorf("tab %s: %w", tab.TabID, err)
	}
	return tab, shell, nil
}

// Type types argv into tab, a tab Open returned whose shell is shell, as one
// line quoted for that shell (herdr.ShellCommand); an argv the shell cannot
// pass unchanged types nothing.
func (l TabLauncher) Type(ctx context.Context, tab TabRef, shell string, argv []string) error {
	line, err := herdr.ShellCommand(shell, argv)
	if err != nil {
		return fmt.Errorf("tab %s: %w", tab.TabID, err)
	}
	if err := l.Host.RunInTab(ctx, tab, line); err != nil {
		return fmt.Errorf("tab %s: %w", tab.TabID, err)
	}
	return nil
}

func (l TabLauncher) waitShell(ctx context.Context, tab TabRef) (string, error) {
	ready, poll := l.ShellReady, l.Poll
	if ready <= 0 {
		ready = 30 * time.Second
	}
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	deadline := time.Now().Add(ready)
	for {
		info, err := l.Host.PaneProcessInfo(ctx, tab)
		if err != nil {
			return "", err
		}
		if info.ShellIdle() {
			return info.Foreground[0].Name, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("shell did not reach its prompt within %s", ready)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(poll):
		}
	}
}

// Stop interrupts the Artisan with Ctrl+C in its tab. The tab stays open.
func (r *TabRun) Stop(ctx context.Context) error {
	return r.host.InterruptTab(ctx, r.Tab)
}
