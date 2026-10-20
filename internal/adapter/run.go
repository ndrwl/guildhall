package adapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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

// TabRef names a tab a tabHost created. The value carries no authority: the
// host that created the tab is the only one that accepts it, and a host refuses
// the zero TabRef, a TabRef from another host and one it never returned.
type TabRef struct {
	Session     string
	WorkspaceID string
	TabID       string
	PaneID      string
}

// tabHost is the terminal a TabLauncher drives: it creates tabs and operates
// only on tabs it created. RunInTab success means the terminal accepted the
// line, not that the command ran or finished; InterruptTab sends Ctrl+C and
// proves nothing about what stopped. Every operation honours its context.
type tabHost interface {
	CreateTab(ctx context.Context, spec herdr.TabSpec) (TabRef, error)
	PaneProcessInfo(ctx context.Context, tab TabRef) (herdr.ProcessInfo, error)
	RunInTab(ctx context.Context, tab TabRef, line string) error
	InterruptTab(ctx context.Context, tab TabRef) error
}

// errNotOwned reports a TabRef the host did not create.
var errNotOwned = errors.New("tab was not created by this host")

// HerdrTabs is the tabHost backed by a Herdr client. It keeps the client's
// ownership proof for each tab it created and accepts only those tabs.
type HerdrTabs struct {
	client *herdr.Client
	mu     sync.Mutex
	owned  map[TabRef]herdr.OwnedTab
}

// NewHerdrTabs returns a tabHost for client.
func NewHerdrTabs(client *herdr.Client) *HerdrTabs {
	return &HerdrTabs{client: client, owned: map[TabRef]herdr.OwnedTab{}}
}

// CreateTab creates a tab without taking focus.
func (h *HerdrTabs) CreateTab(ctx context.Context, spec herdr.TabSpec) (TabRef, error) {
	t, err := h.client.TabCreate(ctx, spec)
	if err != nil {
		return TabRef{}, err
	}
	ref := TabRef{Session: t.Session(), WorkspaceID: t.WorkspaceID(), TabID: t.ID(), PaneID: t.PaneID()}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.owned[ref] = t
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

// TabLauncher starts an Artisan in a new, visible Herdr tab (R1.38).
type TabLauncher struct {
	Host        tabHost
	WorkspaceID string
	// ShellReady bounds the wait for the new tab's shell to reach its prompt.
	ShellReady time.Duration
	// Poll is the interval between shell readiness checks.
	Poll time.Duration
}

// TabRun is an Artisan started in a Herdr tab. The tab is a presentation
// binding only (R1.52): its closing or disappearance never completes the run.
type TabRun struct {
	host tabHost
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
	tab, err := l.Host.CreateTab(ctx, herdr.TabSpec{WorkspaceID: l.WorkspaceID, Label: label, Cwd: cmd.Dir, Env: cmd.Env})
	if err != nil {
		return nil, err
	}
	shell, err := l.waitShell(ctx, tab)
	if err != nil {
		return nil, fmt.Errorf("tab %s: %w", tab.TabID, err)
	}
	line, err := herdr.ShellCommand(shell, cmd.Argv)
	if err != nil {
		return nil, fmt.Errorf("tab %s: %w", tab.TabID, err)
	}
	if err := l.Host.RunInTab(ctx, tab, line); err != nil {
		return nil, fmt.Errorf("tab %s: %w", tab.TabID, err)
	}
	return &TabRun{host: l.Host, Tab: tab}, nil
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
