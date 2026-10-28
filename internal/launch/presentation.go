package launch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/interp"
)

// Presentation is the optional terminal boundary for Process. Host creates and
// types into a new attempt-specific workspace; Report registers the newly created pane
// with Herdr. A tab is never used as evidence of worker liveness, and is never
// closed by Process. A durable single-use launch claim prevents duplicate starts
// after uncertain API responses. Recovery reconciles attempt ownership, not panes.
// The pane invokes the same owned launcher through --pane-launch, which detaches
// its lifetime and streams its durable log; losing the pane cannot stop the worker.
type Presentation struct {
	Host   adapter.TabHost
	Report func(context.Context, string, herdr.AgentReport) error
	// CloseAfter is the effective artisan_workspace_close_after setting, passed to
	// the bridge as WorkspaceCloseAfterFlag so its finish line states it; zero is
	// never closing automatically.
	CloseAfter time.Duration
}

// WorkspaceCloseAfterFlag is the internal bridge flag that carries
// Presentation.CloseAfter; it goes with --pane-launch only.
const WorkspaceCloseAfterFlag = "--workspace-close-after"

// FinishLine is the line the bridge prints when its launcher exits. closeAfter
// is the configured close delay: zero says the Herdr workspace stays open, and a
// negative delay, unknown to the bridge, names only the setting.
func FinishLine(closeAfter time.Duration) string {
	const done = "guildhall: Artisan launcher finished; result and work remain in Guildhall."
	switch {
	case closeAfter == 0:
		return done + " This Herdr workspace stays open (artisan_workspace_close_after is 0); close it yourself when done."
	case closeAfter < 0:
		return done + " This Herdr workspace closes automatically after the configured delay (artisan_workspace_close_after)."
	}
	return fmt.Sprintf("%s This Herdr workspace closes automatically in about %s (artisan_workspace_close_after); set it to 0 to keep Herdr workspaces open.", done, closeAfter)
}

// PaneClaim is the attempt-local, single-use presentation launch intent.
// An ambiguous API call leaves it in place: neither a caller restart nor a
// missing pane is permission to launch this attempt again.
const PaneClaim = "pane-launch.json"

// WorkspaceLabel is the label of the Herdr workspace an attempt is presented
// in: "└ <task> · <step> #<n>".
func WorkspaceLabel(k artisan.AttemptKey) string {
	return fmt.Sprintf("└ %s · %s #%d", k.TaskID, k.Step, k.Attempt)
}

// TabLabel is the label of the tab an attempt runs in: "artisan <key>".
func TabLabel(k artisan.AttemptKey) string { return "artisan " + k.String() }

// AttemptWorkspaceLabels are the labels a Herdr workspace created for attempt
// k alone has carried: WorkspaceLabel, and TabLabel, which releases from #140
// to #145 gave the workspace too. Releases before #140 presented an attempt
// as a tab in a shared workspace, whose label is neither.
func AttemptWorkspaceLabels(k artisan.AttemptKey) []string {
	return []string{WorkspaceLabel(k), TabLabel(k)}
}

func (p *Process) launchPane(ctx context.Context, at Attempt, cmd *exec.Cmd, presentation *Presentation) (interp.Handle, error) {
	if presentation.Host == nil || presentation.Report == nil {
		return interp.Handle{}, errors.New("launch: incomplete pane presentation")
	}
	// Reconcile durable ownership before opening any terminal. In particular a
	// live or unverified owner can never be replaced because its pane disappeared.
	if owner, err := at.owner(); err != nil || owner != nil {
		classification, statusErr := Status(ctx, at)
		return interp.Handle{}, fmt.Errorf("launch: %s already owned (%+v): %w", at.Key, classification, errors.Join(err, statusErr, errors.New("reconcile with artisan status/resume before retrying")))
	}
	claim, err := os.OpenFile(filepath.Join(at.Dir(), PaneClaim), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		classification, statusErr := Status(ctx, at)
		return interp.Handle{}, fmt.Errorf("launch: pane launch already claimed or unavailable (%+v): %w", classification, errors.Join(err, statusErr))
	}
	if _, err := claim.WriteString("{\"state\":\"launching\"}\n"); err != nil {
		_ = claim.Close()
		return interp.Handle{}, err
	}
	if err := claim.Close(); err != nil {
		return interp.Handle{}, err
	}
	label := TabLabel(at.Key)
	workspace, err := presentation.Host.CreateWorkspace(ctx, WorkspaceLabel(at.Key), cmd.Dir)
	if err != nil {
		return interp.Handle{}, err
	}
	l := adapter.TabLauncher{Host: presentation.Host, WorkspaceID: workspace.ID}
	tab, shell, err := l.Open(ctx, label, cmd.Dir, p.Env)
	if err != nil {
		return interp.Handle{}, err
	}
	b, err := json.Marshal(tab)
	if err != nil {
		return interp.Handle{}, err
	}
	if err := os.WriteFile(filepath.Join(at.Dir(), PaneClaim), b, 0o600); err != nil {
		return interp.Handle{}, err
	}
	if err := presentation.Report(ctx, tab.PaneID, herdr.AgentReport{Source: "guildhall", Agent: "artisan", State: "working", Resume: ResumeArgv(at)}); err != nil {
		return interp.Handle{}, err
	}
	argv := append(append([]string(nil), cmd.Args...), "--pane-launch", WorkspaceCloseAfterFlag, presentation.CloseAfter.String())
	if err := l.Type(ctx, tab, shell, argv); err != nil {
		// Type may have succeeded despite losing its response. Reconcile files and
		// ownership for diagnosis, but never send a second command or stop a worker.
		classification, statusErr := Status(ctx, at)
		return interp.Handle{}, fmt.Errorf("launch: pane command uncertain (%+v): %w", classification, errors.Join(err, statusErr))
	}
	c := &child{at: at, pane: true, exited: make(chan struct{})}
	if err := p.owned(ctx, c); err != nil {
		classification, statusErr := Status(ctx, at)
		return interp.Handle{}, fmt.Errorf("launch: pane ownership uncertain (%+v): %w", classification, errors.Join(err, statusErr))
	}
	h := interp.Handle{Identity: at.Identity(), Ref: fmt.Sprintf("%s pid %d", at.Key, c.identity.PID)}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started == nil {
		p.started = map[interp.Handle]*child{}
	}
	p.started[h] = c
	return h, nil
}

// Present starts argv as a detached owned launcher, appending output to logPath,
// and streams that log to out until it exits. A lost output consumer never stops
// the launcher. argv must be a fully validated artisan start invocation without
// --pane-launch. When the launcher exits it prints FinishLine(closeAfter). It is
// an internal CLI bridge, not a second attempt owner.
func Present(argv, env []string, logPath string, closeAfter time.Duration, out io.Writer) error {
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	cmd, err := startDetached(func() *exec.Cmd {
		c := exec.Command(argv[0], argv[1:]...)
		c.Env = env
		c.Stdout, c.Stderr = log, log
		return c
	})
	_ = log.Close()
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	reader, err := os.Open(filepath.Clean(logPath))
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	for {
		// Output errors are presentation errors only: the child remains owned.
		if _, err := io.Copy(out, reader); err != nil {
			return err
		}
		select {
		case err := <-done:
			_, _ = io.Copy(out, reader)
			_, _ = fmt.Fprintln(out, "\n"+FinishLine(closeAfter))
			return err
		case <-time.After(100 * time.Millisecond):
		}
	}
}
