package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/interp"
)

// LogFile is the launcher process's output, appended in the attempt directory.
const LogFile = "launcher.log"

// Process is the real interp.Launcher: it runs each attempt as its own
// `guildhall artisan run` process, the attempt's owned launcher (Run),
// detached from the caller so the attempt outlives it.
//
// Launch refuses, starting nothing, a spec that interp.Spec.Validate refuses,
// an adapter interp.CheckAdapter refuses or other than "fake" (the only one
// `artisan run` runs), an ended context, and a spec not laid out as its
// manifest's attempt under StateDir: an identity other than the task and
// artisan.AttemptKey.AttemptID, or a result file other than
// artisan.ResultFile in artisan.AttemptKey.Dir. The attempt must already be
// recorded running in Store, as the interpreter records it before launch.
// The process's output is appended to LogFile in the attempt directory.
// Launch returns once the process owns the attempt and has recorded its
// endpoint, so a Stop reaches it, and never waits for the attempt to finish;
// a process that exits first is an error, and one that does not get there
// within StartTimeout is stopped and is an error.
//
// Stop stops the attempt through Stop (the package function) and waits for
// the process Launch started to exit. It refuses a handle this Process did
// not return; stopping again, or after the attempt finished, is not an error.
// Both are safe for concurrent use.
type Process struct {
	// Executable is the guildhall binary; Env is added to its environment.
	Executable string
	Env        []string
	// Store and StateDir are the Director store and the absolute state
	// directory, as `artisan run --store --state` takes them.
	Store, StateDir string
	// StartTimeout bounds Launch's wait for ownership; zero is 30 seconds.
	StartTimeout time.Duration

	mu      sync.Mutex
	started map[interp.Handle]*child
}

var _ interp.Launcher = (*Process)(nil)

// child is one process Launch started.
type child struct {
	at     Attempt
	exited chan struct{}
}

// Launch implements interp.Launcher.Launch; see Process.
func (p *Process) Launch(ctx context.Context, spec interp.Spec) (interp.Handle, error) {
	if err := spec.Validate(); err != nil {
		return interp.Handle{}, fmt.Errorf("launch: %w", err)
	}
	if err := interp.CheckAdapter(spec.Adapter); err != nil {
		return interp.Handle{}, fmt.Errorf("launch: %w", err)
	}
	if spec.Adapter != interp.FakeAdapter {
		return interp.Handle{}, fmt.Errorf("launch: adapter %q: the only adapter is %s", spec.Adapter, interp.FakeAdapter)
	}
	m := spec.Manifest.Identity
	at := Attempt{Store: p.Store, StateDir: p.StateDir, Key: artisan.AttemptKey{Repository: m.Repository, TaskID: m.Task, Step: m.Step, Attempt: m.Attempt}}
	if spec.Identity != at.Identity() || filepath.Clean(spec.ResultFile) != at.Files().ResultFile {
		return interp.Handle{}, fmt.Errorf("launch: spec for %s, result %s, is not attempt %s laid out under %s", spec.Identity, spec.ResultFile, at.Key, p.StateDir)
	}
	if err := ctx.Err(); err != nil {
		return interp.Handle{}, err
	}
	log, err := os.OpenFile(filepath.Join(at.Dir(), LogFile), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return interp.Handle{}, err
	}
	k := at.Key
	cmd := exec.Command(p.Executable, "artisan", "run", "--store", p.Store, "--state", p.StateDir,
		"--repository", k.Repository, "--task", k.TaskID, "--step", k.Step, "--attempt", strconv.Itoa(k.Attempt),
		"--adapter", spec.Adapter, "--prompt", spec.PromptFile, "--workdir", spec.WorkDir)
	cmd.Env = append(os.Environ(), p.Env...)
	cmd.Stdout, cmd.Stderr = log, log
	detach(cmd)
	err = cmd.Start()
	// The child holds its own handle on the log.
	_ = log.Close()
	if err != nil {
		return interp.Handle{}, fmt.Errorf("launch: start %s: %w", k, err)
	}
	c := &child{at: at, exited: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(c.exited) }()
	h := interp.Handle{Identity: spec.Identity, Ref: fmt.Sprintf("%s pid %d", k, cmd.Process.Pid)}
	if err := p.owned(ctx, c, cmd.Process.Pid); err != nil {
		_ = p.stop(c)
		return interp.Handle{}, fmt.Errorf("launch: %s: %w; see %s", k, err, filepath.Join(at.Dir(), LogFile))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started == nil {
		p.started = map[interp.Handle]*child{}
	}
	p.started[h] = c
	return h, nil
}

// owned waits until process pid owns c's attempt with its endpoint recorded.
func (p *Process) owned(ctx context.Context, c *child, pid int) error {
	timeout := p.StartTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		o, err := c.at.owner()
		if err == nil && o != nil && o.LauncherIdentity.PID == pid && o.Endpoint != "" {
			return nil
		}
		select {
		case <-c.exited:
			return errors.New("the launcher exited before owning the attempt")
		case <-ctx.Done():
			return fmt.Errorf("the launcher did not own the attempt: %w", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Stop implements interp.Launcher.Stop; see Process.
func (p *Process) Stop(h interp.Handle) error {
	p.mu.Lock()
	c, ok := p.started[h]
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("launch: handle %s was not returned by this launcher", h.Ref)
	}
	return p.stop(c)
}

// stop stops c's attempt and waits for its process to exit, then checks again that it is stopped.
func (p *Process) stop(c *child) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := Stop(ctx, c.at)
	select {
	case <-c.exited:
	case <-ctx.Done():
		return errors.Join(err, fmt.Errorf("launch: the launcher of %s did not exit: %w", c.at.Key, ctx.Err()))
	}
	if errors.Is(err, ErrNotStopped) {
		_, err = Stop(ctx, c.at)
	}
	return err
}

