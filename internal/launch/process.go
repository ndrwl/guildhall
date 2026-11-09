package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/secrets"
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
	// StartTimeout bounds a pane launch's presentation setup, up to typing its command, and a hidden launch's wait
	// for ownership; zero is 30 seconds. A pane launch then waits LockWait plus 10 seconds for ownership, and a
	// hidden launch's wait is at least that long.
	StartTimeout time.Duration
	// LockWait is how long every store open waits on the store's lock, the launcher's too (`artisan start
	// --lock-wait`); zero is the store's default.
	LockWait time.Duration
	// TaskLocked says the caller holds the task lock (artisan.LockAttempt on the task directory) for the whole
	// Launch or LaunchFiles call, so no other launch of this task can be in flight. Only then does a pane launch
	// recover a stale launching PaneClaimFile claim; the zero value never does.
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

// LaunchFiles is Launch for attempt k given as its adapter name, profile, settings, absolute prompt file and absolute
// working directory rather than an interpreter spec, for an attempt the interpreter does not render, such as a
// delivered task's judge (docs/contracts/quality.md); the profile is passed as --profile and the settings as --model
// and --effort, each only when set. It refuses an adapter interp.CheckAdapter refuses, a relative path and an ended
// context, starting nothing, and is otherwise Launch.
func (p *Process) LaunchFiles(ctx context.Context, k artisan.AttemptKey, adapterName, profile string, settings adapter.Settings, promptFile, workDir string) (interp.Handle, error) {
	return p.launchFiles(ctx, k, 0, adapterName, promptFile, workDir, p.Copy, profile, settings.Model, settings.Effort)
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
		// The launcher waits on the store's lock as long as every other caller, never only the store's default.
		if p.LockWait > 0 {
			cmd.Args = append(cmd.Args, "--lock-wait", p.LockWait.String())
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
		// The launcher gets no injected secret; it decrypts its profile's inherit_env names itself.
		cmd.Env = append(append(secrets.Strip(os.Environ()), p.Env...), k.Env())
		cmd.Stdout, cmd.Stderr = log, log
		return cmd
	}
	// In pane mode StartTimeout bounds only the terminal's setup; the wait for ownership after typing has its own
	// budget, from the caller's own context.
	caller := ctx
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
		return p.launchPane(ctx, caller, at, cmd, presentation)
	}
	cmd, err := startDetached(build)
	// The child holds its own handle on the log.
	_ = log.Close()
	if err != nil {
		return interp.Handle{}, fmt.Errorf("launch: start %s: %w", k, err)
	}
	if afterLauncherStart != nil {
		afterLauncherStart(cmd.Process.Pid)
	}
	// The launcher may already have owned, finished and exited. It is read as a child, live or exited, which is
	// safe only because nothing reaps it until the cmd.Wait below starts: keep this lookup before it.
	id, identityErr := artisan.LookupChild(cmd.Process.Pid)
	c := &child{at: at, identity: id, exited: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(c.exited) }()
	h := interp.Handle{Identity: at.Identity(), Ref: fmt.Sprintf("%s pid %d", k, cmd.Process.Pid)}
	if err := errors.Join(identityErr, p.owned(ctx, c, max(p.startTimeout(), p.ownershipWait()))); err != nil {
		if !errors.Is(err, errReleased) {
			_ = p.stop(c, true)
		}
		// Every failure names the log once; owned's "exited before owning" already does.
		logPath := filepath.Join(at.Dir(), LogFile)
		if strings.Contains(err.Error(), logPath) {
			return interp.Handle{}, fmt.Errorf("launch: %s: %w", k, err)
		}
		return interp.Handle{}, fmt.Errorf("launch: %s: %w; see %s", k, err, logPath)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started == nil {
		p.started = map[interp.Handle]*child{}
	}
	p.started[h] = c
	return h, nil
}

// afterLauncherStart, set only by tests, runs after a hidden launcher starts and before Launch reads its identity.
var afterLauncherStart func(pid int)

// errReleased is owned's error for a launcher that finished its attempt before recording its endpoint: the
// attempt failed at setup and there is nothing to stop, and the launcher's advance waits for the caller's lock.
var errReleased = errors.New("the launcher finished the attempt before recording its endpoint")

func (p *Process) startTimeout() time.Duration {
	if p.StartTimeout > 0 {
		return p.StartTimeout
	}
	return 30 * time.Second
}

// ownershipSlack is how much longer than LockWait a caller waits for its launcher to own the attempt, so it
// always outwaits a launcher that is itself waiting LockWait on the store.
const ownershipSlack = 10 * time.Second

// ownershipWait is how long a pane launch waits for ownership once its command is typed: LockWait plus
// ownershipSlack. A hidden launch waits at least this long.
func (p *Process) ownershipWait() time.Duration { return p.LockWait + ownershipSlack }

// PaneStartBudget is how long a pane launch over lockWait may take before a bound claim with no owner is no longer
// one still starting: the default StartTimeout for its setup, and the wait for ownership after typing.
func PaneStartBudget(lockWait time.Duration) time.Duration {
	return (&Process{}).startTimeout() + (&Process{LockWait: lockWait}).ownershipWait()
}

// owned waits, within budget, until c owns its attempt with its endpoint recorded, and fails at once when c
// released it without one, or exited without owning it: a hidden child's exit, or a pane child whose bridge
// recorded a launcher (PaneLauncherFile) proven gone with no ownership recorded.
// A cancelled wait is never reported ready, even if ownership was recorded before the next read.
func (p *Process) owned(ctx context.Context, c *child, budget time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, budget)
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
			return fmt.Errorf("the launcher exited before owning the attempt; see %s", filepath.Join(c.at.Dir(), LogFile))
		}
		// A pane child's exit is seen through the launcher its bridge recorded; the record is read again once more
		// below, as after a hidden child's exit, so a fast launcher that owned and finished is not misreported.
		if c.pane && c.identity.PID == 0 && err == nil && o == nil {
			if state, _ := PaneLauncherState(c.at.Dir()); state == PaneLauncherGone {
				exited = true
				continue
			}
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
