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
// terminal boundary adapter.TabLauncher drives: it creates tabs only in the
// workspaces its terminal holds, operates only on tabs it created, refuses anything but one
// non-empty line and honours each call's context. A line typed into a pane
// runs as `sh -c <line>` with the POSIX sh on PATH, in the tab's working
// directory with the tab's environment added; the pane is busy until that
// line's process has exited. Ctrl+C sends SIGINT to the line's process group
// and waits briefly for it to exit, repeating the signal meanwhile because one
// sent while the line is still starting can be lost; it proves nothing about
// what stopped. The
// pane's shell is represented by the host process, so an idle pane reports it
// as its shell and only foreground process. A line typed while another runs
// is input to that line's program: it is logged and, as a program that does
// not read its terminal would, ignored. Hosts from Reopen share the panes and
// own only the tabs they create or adopt.
type ShellPanes struct {
	session, workspace string
	*shellTerminal
	owned map[adapter.TabRef]bool
}

// shellTerminal is the state hosts on one terminal share, under mu.
type shellTerminal struct {
	mu         sync.Mutex
	workspaces []herdr.Workspace
	// made counts the workspaces ever created, so a closed one's id is never reused.
	made  int
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

// NewShellPanes returns a ShellPanes for session whose terminal starts with
// one workspace, labelled with its id, and kills any line still running when
// t ends.
func NewShellPanes(t *testing.T, session, workspace string) *ShellPanes {
	p := &ShellPanes{session: session, workspace: workspace, owned: map[adapter.TabRef]bool{},
		shellTerminal: &shellTerminal{workspaces: []herdr.Workspace{{ID: workspace, Label: workspace}}, made: 1, panes: map[adapter.TabRef]*shellPane{}}}
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

// Workspaces lists the terminal's workspaces.
func (p *ShellPanes) Workspaces(ctx context.Context) ([]herdr.Workspace, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.workspaces), nil
}

// CreateWorkspace creates a workspace on the terminal, labelled label. Unlike
// Herdr's, it holds no first tab of its own.
func (p *ShellPanes) CreateWorkspace(ctx context.Context, label, cwd string) (herdr.Workspace, error) {
	if err := ctx.Err(); err != nil {
		return herdr.Workspace{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.made++
	w := herdr.Workspace{ID: fmt.Sprintf("%s-%d", p.workspace, p.made), Label: label}
	p.workspaces = append(p.workspaces, w)
	return w, nil
}

// CreateTab creates an idle pane in one of the terminal's workspaces.
func (p *ShellPanes) CreateTab(ctx context.Context, spec herdr.TabSpec) (adapter.TabRef, error) {
	if err := ctx.Err(); err != nil {
		return adapter.TabRef{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !slices.ContainsFunc(p.workspaces, func(w herdr.Workspace) bool { return w.ID == spec.WorkspaceID }) {
		return adapter.TabRef{}, &herdr.APIError{Code: "workspace_not_found", Message: "workspace not found"}
	}
	p.next++
	ref := adapter.TabRef{Session: p.session, WorkspaceID: spec.WorkspaceID,
		TabID: fmt.Sprintf("%s:t%d", spec.WorkspaceID, p.next), PaneID: fmt.Sprintf("%s:p%d", spec.WorkspaceID, p.next)}
	p.panes[ref] = &shellPane{spec: spec}
	p.owned[ref] = true
	return ref, nil
}

// Reopen returns a new host on p's panes, as a later process would make, that
// owns none of them until it adopts them.
func (p *ShellPanes) Reopen() *ShellPanes {
	return &ShellPanes{session: p.session, workspace: p.workspace, shellTerminal: p.shellTerminal, owned: map[adapter.TabRef]bool{}}
}

// AdoptTab accepts a pane this terminal holds in the host's session and workspace.
func (p *ShellPanes) AdoptTab(ctx context.Context, ref adapter.TabRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.panes[ref]; !ok || ref.Session != p.session {
		return &herdr.APIError{Code: "tab_not_found", Message: "tab " + ref.TabID + " not found"}
	}
	p.owned[ref] = true
	return nil
}

// pane returns a tab this host created, under the lock, and ends a line
// that has exited.
func (p *ShellPanes) pane(ctx context.Context, ref adapter.TabRef) (*shellPane, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sp, ok := p.panes[ref]
	if !ok || !p.owned[ref] {
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
	p.sent = append(p.sent, "run "+ref.PaneID+" "+line)
	if sp.cmd != nil {
		return nil
	}
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
		case <-time.After(100 * time.Millisecond): // timing:allow retry cadence; interruptWait is the completion ceiling.
		}
	}
}

var _ adapter.AbandonedWorkspaceCloser = (*ShellPanes)(nil)

// CloseAbandonedWorkspace closes a workspace a failed launch recorded, as
// adapter.AbandonedWorkspaceCloser describes. Its workspaces hold no starting
// tab, so every recorded tab must be labelled tabLabel. It adopts the recorded
// tabs still held and closes the workspace with them; a workspace holding no
// tab, as a launch that failed before creating one leaves, closes by its
// recorded id.
func (p *ShellPanes) CloseAbandonedWorkspace(ctx context.Context, workspaceID string, tabs []adapter.TabRef, workspaceLabels []string, tabLabel string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(workspaceLabels) == 0 || tabLabel == "" {
		return errors.New("close abandoned workspace: no labels to match")
	}
	for _, ref := range tabs {
		if ref.Session != p.session || ref.WorkspaceID != workspaceID {
			return &adapter.LeftOpenError{Reason: fmt.Sprintf("recorded tab %s is in session %q workspace %s, not %q %s", ref.TabID, ref.Session, ref.WorkspaceID, p.session, workspaceID)}
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	i := slices.IndexFunc(p.workspaces, func(w herdr.Workspace) bool { return w.ID == workspaceID })
	if i < 0 {
		return nil
	}
	if label := p.workspaces[i].Label; !slices.Contains(workspaceLabels, label) {
		return &adapter.LeftOpenError{Reason: fmt.Sprintf("workspace %s is labelled %q, not an attempt's workspace label", workspaceID, label)}
	}
	var held []adapter.TabRef
	for ref, sp := range p.panes {
		if ref.WorkspaceID != workspaceID {
			continue
		}
		switch {
		case !slices.Contains(tabs, ref):
			return &adapter.LeftOpenError{Reason: fmt.Sprintf("workspace %s holds tab %s, which the launch did not record", workspaceID, ref.TabID)}
		case sp.spec.Label != tabLabel:
			return &adapter.LeftOpenError{Reason: fmt.Sprintf("tab %s is labelled %q, not %q", ref.TabID, sp.spec.Label, tabLabel)}
		}
		held = append(held, ref)
	}
	for _, ref := range held {
		p.owned[ref] = true
		if sp, err := p.pane(ctx, ref); err != nil {
			return err
		} else if sp.cmd != nil {
			return &adapter.LeftOpenError{Reason: fmt.Sprintf("pane %s is not an idle shell", ref.PaneID)}
		}
	}
	for _, ref := range held {
		delete(p.panes, ref)
		delete(p.owned, ref)
	}
	p.workspaces = slices.Delete(p.workspaces, i, i+1)
	return nil
}

// Sent returns the lines and keys that reached the host's panes, in order.
func (p *ShellPanes) Sent() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.sent)
}
