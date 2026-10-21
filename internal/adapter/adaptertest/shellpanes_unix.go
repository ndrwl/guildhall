//go:build unix

package adaptertest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/herdr"
)

// ShellPanes is a terminal host for running a runtime in a Herdr pane where
// no Herdr server is available, as in CI. It keeps the contract of the
// terminal boundary adapter.TabLauncher drives: it creates tabs only in its
// one workspace, operates only on tabs it created, refuses anything but one
// non-empty line and honours each call's context. A line typed into a pane
// runs as `sh -c <line>` with the POSIX sh on PATH, in the tab's working
// directory with the tab's environment added; the pane is busy until that
// line's process has exited. Ctrl+C sends SIGINT to the line's process group
// and waits briefly for it to exit, repeating the signal meanwhile because one
// sent while the line is still starting can be lost; it proves nothing about
// what stopped. The
// pane's shell is represented by the host process, so an idle pane reports it
// as its shell and only foreground process.
type ShellPanes struct {
	session, workspace string

	mu    sync.Mutex
	next  int
	panes map[adapter.TabRef]*shellPane
	sent  []string
}

// shellPane is one tab and the line running in it, if any.
type shellPane struct {
	spec herdr.TabSpec
	cmd  *exec.Cmd
	done chan struct{}
}

// interruptWait bounds Ctrl+C's wait for the line to exit.
const interruptWait = 10 * time.Second

// NewShellPanes returns a ShellPanes for session with the one workspace, and
// kills any line still running when t ends.
func NewShellPanes(t *testing.T, session, workspace string) *ShellPanes {
	p := &ShellPanes{session: session, workspace: workspace, panes: map[adapter.TabRef]*shellPane{}}
	t.Cleanup(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, sp := range p.panes {
			if sp.cmd != nil {
				_ = syscall.Kill(-sp.cmd.Process.Pid, syscall.SIGKILL)
				<-sp.done
			}
		}
	})
	return p
}

// CreateTab creates an idle pane in the host's workspace.
func (p *ShellPanes) CreateTab(ctx context.Context, spec herdr.TabSpec) (adapter.TabRef, error) {
	if err := ctx.Err(); err != nil {
		return adapter.TabRef{}, err
	}
	if spec.WorkspaceID != p.workspace {
		return adapter.TabRef{}, &herdr.APIError{Code: "workspace_not_found", Message: "workspace not found"}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	ref := adapter.TabRef{Session: p.session, WorkspaceID: p.workspace,
		TabID: fmt.Sprintf("%s:t%d", p.workspace, p.next), PaneID: fmt.Sprintf("%s:p%d", p.workspace, p.next)}
	p.panes[ref] = &shellPane{spec: spec}
	return ref, nil
}

// pane returns a tab this host created, under the lock, and ends a line
// that has exited.
func (p *ShellPanes) pane(ctx context.Context, ref adapter.TabRef) (*shellPane, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sp, ok := p.panes[ref]
	if !ok {
		return nil, fmt.Errorf("tab %q in session %q was not created by this host", ref.TabID, ref.Session)
	}
	if sp.cmd != nil {
		select {
		case <-sp.done:
			sp.cmd, sp.done = nil, nil
		default:
		}
	}
	return sp, nil
}

// PaneProcessInfo reports the pane's shell and foreground processes.
func (p *ShellPanes) PaneProcessInfo(ctx context.Context, ref adapter.TabRef) (herdr.ProcessInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	sp, err := p.pane(ctx, ref)
	if err != nil {
		return herdr.ProcessInfo{}, err
	}
	shell := herdr.Process{PID: os.Getpid(), Name: "sh", Argv: []string{"sh"}}
	if sp.cmd == nil {
		return herdr.ProcessInfo{ShellPID: shell.PID, Foreground: []herdr.Process{shell}}, nil
	}
	return herdr.ProcessInfo{ShellPID: shell.PID, Foreground: []herdr.Process{{PID: sp.cmd.Process.Pid, Name: "sh", Argv: sp.cmd.Args}}}, nil
}

// RunInTab runs one line in an idle pane.
func (p *ShellPanes) RunInTab(ctx context.Context, ref adapter.TabRef, line string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	sp, err := p.pane(ctx, ref)
	if err != nil {
		return err
	}
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return errors.New("a pane runs exactly one non-empty line")
	}
	if sp.cmd != nil {
		return fmt.Errorf("pane %s is still running a line", ref.PaneID)
	}
	p.sent = append(p.sent, "run "+ref.PaneID+" "+line)
	c := exec.Command("sh", "-c", line)
	c.Dir = sp.spec.Cwd
	c.Env = append(os.Environ(), sp.spec.Env...)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() { _ = c.Wait(); close(done) }()
	sp.cmd, sp.done = c, done
	return nil
}

// InterruptTab sends Ctrl+C to the pane: SIGINT to a running line's process
// group, repeated until the line exits or a bounded wait ends.
func (p *ShellPanes) InterruptTab(ctx context.Context, ref adapter.TabRef) error {
	p.mu.Lock()
	sp, err := p.pane(ctx, ref)
	if err != nil {
		p.mu.Unlock()
		return err
	}
	p.sent = append(p.sent, "keys "+ref.PaneID+" ctrl+c")
	c, done := sp.cmd, sp.done
	p.mu.Unlock()
	if c == nil {
		return nil
	}
	deadline := time.After(interruptWait)
	for {
		if err := syscall.Kill(-c.Process.Pid, syscall.SIGINT); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return nil
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Sent returns the lines and keys that reached the host's panes, in order.
func (p *ShellPanes) Sent() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.sent)
}
