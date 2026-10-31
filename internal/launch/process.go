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
	"github.com/ndrwl/guildhall/internal/store"
)

// LogFile is the launcher process's output, appended in the attempt directory.
const LogFile = "launcher.log"

// Process is the real interp.Launcher: it runs each attempt as its own
// `guildhall artisan start` process, the attempt's owned launcher (Run),
// detached from the caller so the attempt outlives it.
//
// Launch refuses, starting nothing, a spec that interp.Spec.Validate refuses,
// an adapter interp.CheckAdapter refuses, an ended context, and a spec whose
// interp.Spec.AdapterSpec is not laid out as its manifest's attempt under StateDir: an identity other than the task and
// artisan.AttemptKey.AttemptID, or a result file other than
// artisan.ResultFile in artisan.AttemptKey.Dir. The attempt must already be
// recorded running in Store, as the interpreter records it before launch.
// The process gets the adapter name unchanged and the prompt file, working
// directory, copy/profile reference and settings (as --model and --effort, each
// only when set) of interp.Spec.AdapterSpec, the mapping the adapter conformance
// suite runs runtimes on; `artisan start` looks the name up in package
// runtimes, and a name it refuses, like every other startup failure, is the
// process exiting before it owns the attempt.
// The process's output is appended to LogFile in the attempt directory.
// Launch returns once the process owns the attempt and has recorded its
// endpoint, so a Stop reaches it, and never waits for the attempt to finish;
// a process that exits first is an error, one that finishes the attempt
// before recording its endpoint is an error at once, and one that does not
// get there within StartTimeout or before ctx ends is stopped and is an
// error, Launch waiting only until it releases the attempt, not for it to exit.
//
// Stop asks only the process Launch started to stop through its endpoint and
// waits for that process to exit. It refuses a handle this Process did
// not return; stopping again, or after the attempt finished, is not an error.
// Both are safe for concurrent use.
//
// With Presentation or OpenPresentation set, the pane starts the ordinary
// detached launcher through a log-streaming bridge. Launch's single-use claim
// and durable owner identity, never pane liveness, decide whether a command may
// be typed. Unlike hidden-mode cleanup, an ambiguous pane start is left owned
// for reconciliation, never stopped or retried speculatively. Tabs stay open.
type Process struct {
	// Presentation, when set, starts the owned launcher from a new Artisan pane.
	// With nil and no OpenPresentation, Launch retains hidden detached mode.
	Presentation *Presentation
	// OpenPresentation lazily resolves the terminal only when launching work,
	// never while recording results, cancelling, or reconciling an attempt.
	// It is ignored when Presentation is set; a nil result means hidden mode.
	OpenPresentation func(context.Context) (*Presentation, error)
	// Executable is the guildhall binary; Env is added to its environment.
	Executable string
	Env        []string
	// Store and StateDir are the Director store and the absolute state
	// directory, as `artisan start --store --state` takes them.
	Store, StateDir string
	// Copy is the Director copy for LaunchFiles; Launch uses its adapter spec's copy.
	Copy string
	// StallInterval, when positive, is passed as `--stall-interval`, so the launcher sends the Director
	// worker-stalled from its heartbeat (Config.StallInterval); the engine passes its own.
	StallInterval time.Duration
	// StartTimeout bounds presentation setup and Launch's wait for ownership; zero is 30 seconds.
	StartTimeout time.Duration
	LockWait     time.Duration
	// TaskLocked says the caller holds the task lock (artisan.LockAttempt on the task directory) for the whole
	// Launch or LaunchFiles call, so no other launch of this task can be in flight. Only then does a pane launch
	// recover a stale launching PaneClaim; the zero value never does.
	TaskLocked bool

	mu      sync.Mutex
	started map[interp.Handle]*child
}

var _ interp.Launcher = (*Process)(nil)

// child is one process Launch started.
type child struct {
	at       Attempt
	identity artisan.Identity
	exited   chan struct{}
	pane     bool
}

// Launch implements interp.Launcher.Launch; see Process.
func (p *Process) Launch(ctx context.Context, spec interp.Spec) (interp.Handle, error) {
	if err := spec.Validate(); err != nil {
		return interp.Handle{}, fmt.Errorf("launch: %w", err)
	}
	if err := interp.CheckAdapter(spec.Adapter); err != nil {
		return interp.Handle{}, fmt.Errorf("launch: %w", err)
	}
	m := spec.Manifest.Identity
	at := Attempt{Store: p.Store, StateDir: p.StateDir, LockWait: p.LockWait, Key: artisan.AttemptKey{Repository: m.Repository, TaskID: m.Task, Step: m.Step, Attempt: m.Attempt}}
	as := spec.AdapterSpec()
	if as.Identity != at.Identity() || filepath.Clean(as.ResultFile) != at.Files().ResultFile {
		return interp.Handle{}, fmt.Errorf("launch: spec for %s, result %s, is not attempt %s laid out under %s", as.Identity, as.ResultFile, at.Key, p.StateDir)
	}
	return p.launchFiles(ctx, at.Key, spec.Slices, spec.Adapter, as.PromptFile, as.WorkDir, as.Copy, as.Profile, as.Model, as.Effort)
}

// LaunchFiles is Launch for attempt k given as its adapter name, absolute prompt file and absolute working directory
// rather than an interpreter spec, for an attempt the interpreter does not render, such as a delivered task's judge
// (docs/contracts/quality.md); it refuses an adapter interp.CheckAdapter refuses, a relative path and an ended
// context, starting nothing, and is otherwise Launch.
func (p *Process) LaunchFiles(ctx context.Context, k artisan.AttemptKey, adapterName, promptFile, workDir string) (interp.Handle, error) {
	return p.launchFiles(ctx, k, 0, adapterName, promptFile, workDir, p.Copy, "", "", "")
}

func (p *Process) launchFiles(ctx context.Context, k artisan.AttemptKey, slices int, adapterName, promptFile, workDir, copyDir, profile, model, effort string) (interp.Handle, error) {
	if err := interp.CheckAdapter(adapterName); err != nil {
		return interp.Handle{}, fmt.Errorf("launch: %w", err)
	}
	if !filepath.IsAbs(promptFile) || !filepath.IsAbs(workDir) {
		return interp.Handle{}, fmt.Errorf("launch: prompt file %q and working directory %q must be absolute", promptFile, workDir)
	}
	storePath, err := filepath.Abs(p.Store)
	if err != nil {
		return interp.Handle{}, fmt.Errorf("launch: store path: %w", err)
	}
	at := Attempt{Store: storePath, StateDir: p.StateDir, LockWait: p.LockWait, Key: k, Slices: slices}
	if err := ctx.Err(); err != nil {
		return interp.Handle{}, err
	}
	log, err := os.OpenFile(filepath.Join(at.Dir(), LogFile), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return interp.Handle{}, err
	}
	build := func() *exec.Cmd {
		cmd := exec.Command(p.Executable, "artisan", "start", "--store", at.Store, "--state", p.StateDir,
			"--repo", k.Repository, "--task", k.TaskID, "--step", k.Step, "--attempt", strconv.Itoa(k.Attempt),
			"--adapter", adapterName, "--prompt", promptFile, "--workdir", workDir)
		if copyDir != "" {
			cmd.Args = append(cmd.Args, "--copy", copyDir, "--advance")
		}
		if p.StallInterval > 0 {
			cmd.Args = append(cmd.Args, "--stall-interval", p.StallInterval.String())
		}
		if profile != "" {
			cmd.Args = append(cmd.Args, "--profile", profile)
		}
		if model != "" {
			cmd.Args = append(cmd.Args, "--model", model)
		}
		if effort != "" {
			cmd.Args = append(cmd.Args, "--effort", effort)
		}
		cmd.Env = append(append(os.Environ(), p.Env...), k.Env())
		cmd.Stdout, cmd.Stderr = log, log
		return cmd
	}
	if p.Presentation != nil || p.OpenPresentation != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.startTimeout())
		defer cancel()
	}
	presentation := p.Presentation
	if presentation == nil && p.OpenPresentation != nil {
		presentation, err = p.OpenPresentation(ctx)
		if err != nil {
			_ = log.Close()
			return interp.Handle{}, err
		}
	}
	if presentation != nil {
		_ = log.Close()
		cmd := build()
		cmd.Dir = workDir
		return p.launchPane(ctx, at, cmd, presentation)
	}
	cmd, err := startDetached(build)
	// The child holds its own handle on the log.
	_ = log.Close()
	if err != nil {
		return interp.Handle{}, fmt.Errorf("launch: start %s: %w", k, err)
	}
	id, identityErr := artisan.Lookup(cmd.Process.Pid)
	c := &child{at: at, identity: id, exited: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(c.exited) }()
	h := interp.Handle{Identity: at.Identity(), Ref: fmt.Sprintf("%s pid %d", k, cmd.Process.Pid)}
	if err := errors.Join(identityErr, p.owned(ctx, c)); err != nil {
		if !errors.Is(err, errReleased) {
			_ = p.stop(c, true)
		}
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

// errReleased is owned's error for a launcher that finished its attempt before recording its endpoint: the
// attempt failed at setup and there is nothing to stop, and the launcher's advance waits for the caller's lock.
var errReleased = errors.New("the launcher finished the attempt before recording its endpoint")

func (p *Process) startTimeout() time.Duration {
	if p.StartTimeout > 0 {
		return p.StartTimeout
	}
	return 30 * time.Second
}

// owned waits until c owns its attempt with its endpoint recorded, and fails at once when c released it without one.
// A cancelled wait is never reported ready, even if ownership was recorded before the next read.
func (p *Process) owned(ctx context.Context, c *child) error {
	ctx, cancel := context.WithTimeout(ctx, p.startTimeout())
	defer cancel()
	exited := false
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("the launcher did not own the attempt: %w", err)
		}
		o, err := c.at.owner()
		// Opening the store may wait on its lock past the caller's deadline.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("the launcher did not own the attempt: %w", ctxErr)
		}
		if err == nil && o != nil && c.pane && c.identity.PID == 0 {
			c.identity = artisan.Identity(o.LauncherIdentity)
			go func() {
				for {
					if live, _ := artisan.Probe(c.identity); live == artisan.Gone {
						close(c.exited)
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
			}()
		}
		if err == nil && o != nil && artisan.Identity(o.LauncherIdentity) == c.identity {
			if o.Endpoint != "" {
				return nil
			}
			if o.State == store.OwnerReleased {
				return errReleased
			}
		}
		if exited {
			return errors.New("the launcher exited before owning the attempt")
		}
		select {
		case <-c.exited:
			// A fast launcher can record ownership and finish after the read
			// above. Read its durable record again before reporting failure.
			exited = true
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
	return p.stop(c, false)
}

// stop asks c's endpoint to stop and waits for its process to exit or, with released, for it to release its
// attempt: Launch's caller holds the task lock the launcher's advance then waits for.
func (p *Process) stop(c *child, released bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	called := false
	for {
		o, err := c.at.owner()
		if err != nil {
			return err
		}
		mine := o != nil && artisan.Identity(o.LauncherIdentity) == c.identity
		if released && mine && o.State == store.OwnerReleased {
			return nil
		}
		select {
		case <-c.exited:
			if mine {
				if live, err := ProbeOwner(*o); err != nil || live != store.ProbeGone {
					return errors.Join(ErrNotStopped, err)
				}
			}
			return nil
		default:
		}
		if mine && o.Endpoint != "" && !called {
			_, err := artisan.Call(ctx, o.Endpoint, c.at.Key, c.identity, artisan.OpStop)
			if released {
				called = true
				continue
			}
			select {
			case <-c.exited:
				if err != nil && !errors.Is(err, artisan.ErrEndpointRequest) {
					if live, probeErr := ProbeOwner(*o); probeErr == nil && live == store.ProbeGone {
						return nil
					}
				}
				return err
			case <-ctx.Done():
				return errors.Join(err, ctx.Err())
			}
		}
		select {
		case <-c.exited:
			continue
		case <-ctx.Done():
			return fmt.Errorf("launch: the launcher of %s did not exit: %w", c.at.Key, ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
