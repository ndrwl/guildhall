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

// tabHost is the part of the Herdr client a TabLauncher uses.
type tabHost interface {
	TabCreate(ctx context.Context, spec herdr.TabSpec) (herdr.OwnedTab, error)
	PaneProcessInfo(ctx context.Context, paneID string) (herdr.ProcessInfo, error)
	RunInOwnedTab(ctx context.Context, t herdr.OwnedTab, line string) error
	InterruptOwnedTab(ctx context.Context, t herdr.OwnedTab) error
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
	Tab  herdr.OwnedTab
}

// Start creates a tab labelled label in the launcher's workspace, waits until
// its shell is the only foreground process, and types cmd into it, quoted for
// that shell. A tab whose shell is unsupported or never becomes idle is left
// open with nothing typed into it.
func (l TabLauncher) Start(ctx context.Context, label string, cmd Command) (*TabRun, error) {
	if len(cmd.Argv) == 0 {
		return nil, errors.New("artisan command is empty")
	}
	tab, err := l.Host.TabCreate(ctx, herdr.TabSpec{WorkspaceID: l.WorkspaceID, Label: label, Cwd: cmd.Dir, Env: cmd.Env})
	if err != nil {
		return nil, err
	}
	shell, err := l.waitShell(ctx, tab)
	if err != nil {
		return nil, fmt.Errorf("tab %s: %w", tab.ID(), err)
	}
	line, err := herdr.ShellCommand(shell, cmd.Argv)
	if err != nil {
		return nil, fmt.Errorf("tab %s: %w", tab.ID(), err)
	}
	if err := l.Host.RunInOwnedTab(ctx, tab, line); err != nil {
		return nil, fmt.Errorf("tab %s: %w", tab.ID(), err)
	}
	return &TabRun{host: l.Host, Tab: tab}, nil
}

func (l TabLauncher) waitShell(ctx context.Context, tab herdr.OwnedTab) (string, error) {
	ready, poll := l.ShellReady, l.Poll
	if ready <= 0 {
		ready = 30 * time.Second
	}
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	deadline := time.Now().Add(ready)
	for {
		info, err := l.Host.PaneProcessInfo(ctx, tab.PaneID())
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
	return r.host.InterruptOwnedTab(ctx, r.Tab)
}
