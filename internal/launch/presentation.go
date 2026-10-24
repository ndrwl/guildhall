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
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/interp"
)

// Presentation is the optional terminal boundary for Process. Host creates and
// types into the Director's workspace; Report registers the newly created pane
// with Herdr. A tab is never used as evidence of worker liveness, and is never
// closed by Process. A durable single-use launch claim prevents duplicate starts
// after uncertain API responses. Recovery reconciles attempt ownership, not panes.
// The pane invokes the same owned launcher through --pane-launch, which detaches
// its lifetime and streams its durable log; losing the pane cannot stop the worker.
type Presentation struct {
	Host      adapter.TabHost
	Workspace string
	Report    func(context.Context, string, herdr.AgentReport) error
}

// PaneClaim is the attempt-local, single-use presentation launch intent.
// An ambiguous API call leaves it in place: neither a caller restart nor a
// missing pane is permission to launch this attempt again.
const PaneClaim = "pane-launch.json"

func (p *Process) launchPane(ctx context.Context, at Attempt, cmd *exec.Cmd, presentation *Presentation) (interp.Handle, error) {
	if presentation.Host == nil || presentation.Report == nil || presentation.Workspace == "" {
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
	l := adapter.TabLauncher{Host: presentation.Host, WorkspaceID: presentation.Workspace}
	tab, shell, err := l.Open(ctx, "artisan "+at.Key.String(), cmd.Dir, p.Env)
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
	argv := append(append([]string(nil), cmd.Args...), "--pane-launch")
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
// --pane-launch. It is an internal CLI bridge, not a second attempt owner.
func Present(argv, env []string, logPath string, out io.Writer) error {
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
			_, _ = fmt.Fprintln(out, "\nguildhall: Artisan launcher finished; result and work remain in Guildhall. This tab may be closed.")
			return err
		case <-time.After(100 * time.Millisecond):
		}
	}
}
