package artisantest

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
)

// ResultBody is the result a Worker publishes; Files counts only it as valid.
const ResultBody = "succeeded\n"

// Files is the double of artisan.AttemptFiles over a result file and a
// marker file: a result is valid when it holds exactly ResultBody.
type Files struct{ Result, Marker string }

// NewFiles returns the attempt files of the attempt directory dir.
func NewFiles(dir string) Files {
	return Files{Result: filepath.Join(dir, "result"), Marker: filepath.Join(dir, "cancelled")}
}

func (f Files) ValidResult() (bool, error) {
	b, err := os.ReadFile(f.Result)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil && string(b) == ResultBody, err
}

func (f Files) Marked() (bool, error) {
	_, err := os.Stat(f.Marker)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (f Files) Mark() error {
	m, err := os.OpenFile(f.Marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return m.Close()
}

// stopLadder keeps the contract's waits short: a worker ignoring every
// graceful rung takes its interrupt and cooperative bounds to stop.
var stopLadder = artisan.Ladder{Cooperative: 2 * time.Second, Interrupt: 500 * time.Millisecond, Terminate: 2 * time.Second, Kill: 5 * time.Second}

// RunStopContract runs the launcher's stop ladder and precedence contract
// with units from h; the scenario ids are those of the P0 lifecycle plan.
func RunStopContract(t *testing.T, h Harness) {
	t.Run("L6 a cooperative worker stops at rung 1", func(t *testing.T) {
		c := h.launch(t, func(f Files) Worker { return Worker{Grandchildren: 1, CancelMarker: f.Marker} })
		c.start(nil)
		c.role("grandchild", 1)
		c.stop(nil)
		c.outcome(nil, artisan.Outcome{State: artisan.Cancelled, Rung: 1})
		c.marked(true)
		c.event("rung-emptied", 1)
		c.assertGone(c.owned()...)
	})
	t.Run("L7 an ignored interrupt escalates with attribution", func(t *testing.T) {
		c := h.launch(t, func(Files) Worker { return Worker{Grandchildren: 1, IgnoreInterrupt: true} })
		c.start(nil)
		c.role("grandchild", 1)
		c.stop(nil)
		want := 4
		if h.Terminate {
			want = 3
		} else {
			c.event("rung-skipped", 3)
		}
		c.outcome(nil, artisan.Outcome{State: artisan.Cancelled, Rung: want})
		for _, rung := range []int{1, 2} {
			if e := c.event("rung-timeout", rung); len(e.Alive) == 0 {
				t.Errorf("rung %d timed out without naming the processes alive: %+v", rung, e)
			}
		}
		c.assertGone(c.owned()...)
	})
	t.Run("L15 a stop before the worker starts cancels without starting it", func(t *testing.T) {
		c := h.launch(t, func(Files) Worker { return Worker{} })
		stopped := make(chan error, 1)
		go func() { _, err := c.l.Stop(); stopped <- err }()
		c.phase("stopping")
		c.start(nil)
		c.outcome(nil, artisan.Outcome{State: artisan.Cancelled})
		if err := <-stopped; err != nil {
			t.Errorf("Stop() = %v", err)
		}
		c.marked(true)
		if inv, _ := Inventory(c.dir); len(inv) != 0 {
			t.Errorf("a worker started after the stop: %v", inv)
		}
	})
	t.Run("L15 a cancel before the worker starts cancels without starting it", func(t *testing.T) {
		c := h.launch(t, func(Files) Worker { return Worker{} })
		if _, err := c.l.Cancel(); err != nil {
			t.Fatalf("Cancel() = %v", err)
		}
		c.start(nil)
		c.outcome(nil, artisan.Outcome{State: artisan.Cancelled})
		if inv, _ := Inventory(c.dir); len(inv) != 0 {
			t.Errorf("a worker started after the cancel: %v", inv)
		}
	})
	t.Run("L15 a stop during startup leaves no owned process", func(t *testing.T) {
		c := h.launch(t, func(Files) Worker { return Worker{Grandchildren: 1, IgnoreInterrupt: true} })
		c.start(nil)
		c.stop(nil)
		c.outcome(nil, artisan.Outcome{State: artisan.Cancelled, Rung: -1})
		c.assertGone(c.owned()...)
	})
	t.Run("L16 a result before the stop finishes and the stop is a no-op", func(t *testing.T) {
		c := h.launch(t, func(f Files) Worker { return Worker{Result: f.Result} })
		c.start(nil)
		c.waitFile(c.files.Result)
		c.stop(nil)
		c.outcome(nil, artisan.Outcome{State: artisan.Finished, Rung: -1})
		c.marked(false)
	})
	t.Run("L16 a result after the marker is rejected as late", func(t *testing.T) {
		c := h.launch(t, func(f Files) Worker { return Worker{Result: f.Result, ResultMarker: f.Marker} })
		c.start(nil)
		c.role("worker", 1)
		c.stop(nil)
		c.outcome(nil, artisan.Outcome{State: artisan.Cancelled, Rung: 1, LateResultRejected: true})
		c.marked(true)
	})
	t.Run("L16 a result racing the stop is decided exactly once", func(t *testing.T) {
		for range 4 {
			c := h.launch(t, func(f Files) Worker { return Worker{Result: f.Result, ResultAfter: 100 * time.Millisecond} })
			c.start(nil)
			c.role("worker", 1)
			time.Sleep(100 * time.Millisecond)
			c.stop(nil)
			out := c.outcome(nil, artisan.Outcome{Rung: -1, State: "any"})
			c.marked(out.State == artisan.Cancelled)
			if out.State == artisan.Cancelled {
				valid, _ := c.files.ValidResult()
				if valid != out.LateResultRejected {
					t.Errorf("cancelled with a result present %v, late result rejected %v", valid, out.LateResultRejected)
				}
			}
			c.count("ended", 1)
		}
	})
	t.Run("L17 a result after cancel is rejected and the worker exits", func(t *testing.T) {
		c := h.launch(t, func(f Files) Worker { return Worker{Result: f.Result, ResultMarker: f.Marker} })
		c.start(nil)
		c.role("worker", 1)
		if _, err := c.l.Cancel(); err != nil {
			t.Fatalf("Cancel() = %v", err)
		}
		c.marked(true)
		c.outcome(nil, artisan.Outcome{State: artisan.Cancelled, LateResultRejected: true})
		c.count("ladder", 0)
		c.phase("ended")
		c.assertGone(c.owned()...)
	})
	t.Run("L18 concurrent and repeated stops share one ladder", func(t *testing.T) {
		c := h.launch(t, func(Files) Worker { return Worker{Grandchildren: 1, IgnoreInterrupt: true} })
		c.start(nil)
		c.role("grandchild", 1)
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() { c.stop(nil) })
		}
		wg.Wait()
		c.outcome(nil, artisan.Outcome{State: artisan.Cancelled, Rung: -1})
		begun := time.Now()
		c.stop(nil)
		if d := time.Since(begun); d > time.Second {
			t.Errorf("a stop after the end took %v", d)
		}
		c.count("ladder", 1)
		c.count("ended", 1)
		if n := c.u.calls("kill"); n > 1 {
			t.Errorf("Kill called %d times", n)
		}
		if n := c.u.calls("interrupt"); n != 1 {
			t.Errorf("Interrupt called %d times, want 1", n)
		}
	})
	for _, sig := range []os.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run("a "+sig.String()+" to the launcher stops the attempt", func(t *testing.T) {
			c := h.launch(t, func(f Files) Worker { return Worker{CancelMarker: f.Marker} })
			sigs := make(chan os.Signal, 1)
			c.start(sigs)
			c.role("worker", 1)
			sigs <- sig
			c.outcome(nil, artisan.Outcome{State: artisan.Cancelled, Rung: 1})
		})
	}
	t.Run("an interrupt to the launcher is forwarded to the worker", func(t *testing.T) {
		c := h.launch(t, func(f Files) Worker { return Worker{IgnoreInterrupt: true, CancelMarker: f.Marker} })
		sigs := make(chan os.Signal, 1)
		c.start(sigs)
		c.role("worker", 1)
		sigs <- os.Interrupt
		for end := time.Now().Add(deadline); c.u.calls("interrupt") == 0; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatalf("waited %v for the interrupt to be forwarded", deadline)
			}
		}
		c.phase("running")
		c.marked(false)
		c.stop(nil)
		c.outcome(nil, artisan.Outcome{State: artisan.Cancelled, Rung: 1})
	})
	t.Run("an invalid result does not finish the attempt", func(t *testing.T) {
		c := h.launch(t, func(Files) Worker { return Worker{Lifetime: 500 * time.Millisecond} })
		if err := os.WriteFile(c.files.Result, []byte("garbage"), 0o600); err != nil {
			t.Fatal(err)
		}
		c.start(nil)
		c.outcome(nil, artisan.Outcome{State: artisan.Failed})
	})
	t.Run("an attempt with an outcome is not run again", func(t *testing.T) {
		c := h.launch(t, func(Files) Worker { return Worker{} })
		if err := os.WriteFile(filepath.Join(c.attempt, artisan.OutcomeFile), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := c.l.Run(Command(c.dir, c.w), nil); !errors.Is(err, artisan.ErrDecided) {
			t.Errorf("Run() = %v, want ErrDecided", err)
		}
		if _, err := c.l.Run(Command(c.dir, c.w), nil); !errors.Is(err, artisan.ErrLauncherRan) {
			t.Errorf("second Run() = %v, want ErrLauncherRan", err)
		}
		close(c.done)
		if inv, _ := Inventory(c.dir); len(inv) != 0 {
			t.Errorf("a decided attempt started a worker: %v", inv)
		}
	})
	t.Run("survivors after the last rung are reported", func(t *testing.T) {
		c := h.launch(t, func(Files) Worker { return Worker{IgnoreInterrupt: true} })
		c.u.stuck = true
		c.ladder = artisan.Ladder{Cooperative: 100 * time.Millisecond, Interrupt: 100 * time.Millisecond, Terminate: 100 * time.Millisecond, Kill: 100 * time.Millisecond}
		c.start(nil)
		worker := c.role("worker", 1)[0].Identity
		c.stop(artisan.ErrNotEmpty)
		out := c.outcome(artisan.ErrNotEmpty, artisan.Outcome{State: artisan.Cancelled, Rung: 4})
		if !slices.Contains(out.Survivors, worker) {
			t.Errorf("survivors %v do not name the worker %v", out.Survivors, worker)
		}
	})
}

// launch is one stop-contract case: a run whose unit a Launcher drives.
type launch struct {
	*run
	u       *countingUnit
	attempt string
	files   Files
	ladder  artisan.Ladder
	l       *artisan.Launcher

	done chan struct{}
	out  artisan.Outcome
	err  error
}

func (h Harness) launch(t *testing.T, worker func(Files) Worker) *launch {
	t.Helper()
	attempt := t.TempDir()
	files := NewFiles(attempt)
	w := worker(files)
	if w.Lifetime == 0 {
		w.Lifetime = 30 * time.Second
	}
	c := &launch{run: h.begin(t, w), attempt: attempt, files: files, ladder: stopLadder, done: make(chan struct{})}
	c.u = &countingUnit{Unit: c.run.u, n: map[string]int{}}
	l, err := artisan.NewLauncher(artisan.LauncherConfig{Dir: attempt, Unit: c.u, Files: files, Ladder: stopLadder})
	if err != nil {
		t.Fatalf("NewLauncher() = %v", err)
	}
	c.l = l
	t.Cleanup(func() {
		select {
		case <-c.done:
		case <-time.After(deadline):
			t.Errorf("Run did not return within %v", deadline)
		}
	})
	return c
}

// start starts Run in the background.
func (c *launch) start(signals <-chan os.Signal) {
	if c.ladder != stopLadder {
		l, err := artisan.NewLauncher(artisan.LauncherConfig{Dir: c.attempt, Unit: c.u, Files: c.files, Ladder: c.ladder})
		if err != nil {
			c.t.Fatalf("NewLauncher() = %v", err)
		}
		c.l = l
	}
	go func() {
		defer close(c.done)
		c.out, c.err = c.l.Run(Command(c.dir, c.w), signals)
	}()
}

// stop calls Stop and checks it answers phase "ended" with Run's error.
func (c *launch) stop(want error) {
	c.t.Helper()
	st, err := c.l.Stop()
	if !errors.Is(err, want) || (want == nil && err != nil) {
		c.t.Errorf("Stop() = %v, want %v", err, want)
	}
	if st.Phase != "ended" {
		c.t.Errorf("Stop() phase %q, want ended", st.Phase)
	}
}

// outcome waits for Run and checks its outcome against want: a Rung of -1
// or a State of "any" accepts any value. The outcome file must match.
func (c *launch) outcome(wantErr error, want artisan.Outcome) artisan.Outcome {
	c.t.Helper()
	select {
	case <-c.done:
	case <-time.After(deadline):
		c.t.Fatalf("waited %v for Run to return", deadline)
	}
	if !errors.Is(c.err, wantErr) || (wantErr == nil && c.err != nil) {
		c.t.Errorf("Run() error = %v, want %v", c.err, wantErr)
	}
	got := c.out
	if got.Schema != artisan.OutcomeSchema {
		c.t.Errorf("outcome schema %q, want %q", got.Schema, artisan.OutcomeSchema)
	}
	if (want.State != "any" && (got.State != want.State || got.LateResultRejected != want.LateResultRejected)) || (want.Rung >= 0 && got.Rung != want.Rung) {
		c.t.Errorf("outcome %+v, want state %s rung %d late result rejected %v", got, want.State, want.Rung, want.LateResultRejected)
	}
	b, err := os.ReadFile(filepath.Join(c.attempt, artisan.OutcomeFile))
	var file artisan.Outcome
	if err == nil {
		err = json.Unmarshal(b, &file)
	}
	if err != nil || file.State != got.State || file.Rung != got.Rung || file.LateResultRejected != got.LateResultRejected {
		c.t.Errorf("outcome file %s (%v) does not record %+v", b, err, got)
	}
	return got
}

func (c *launch) marked(want bool) {
	c.t.Helper()
	if got, err := c.files.Marked(); got != want || err != nil {
		c.t.Errorf("cancel marker present %v (%v), want %v", got, err, want)
	}
}

func (c *launch) phase(want string) {
	c.t.Helper()
	var st artisan.EndpointState
	for end := time.Now().Add(deadline); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		var err error
		if st, err = c.l.Status(); err == nil && st.Phase == want {
			return
		}
	}
	c.t.Fatalf("waited %v for phase %q, have %q", deadline, want, st.Phase)
}

func (c *launch) waitFile(path string) {
	c.t.Helper()
	for end := time.Now().Add(deadline); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	c.t.Fatalf("waited %v for %s", deadline, path)
}

// Event is one line of the launcher's events file, as far as the contract
// reads it.
type Event struct {
	Event string             `json:"event"`
	Rung  int                `json:"rung"`
	Alive []artisan.Identity `json:"alive"`
}

func (c *launch) events() []Event {
	c.t.Helper()
	f, err := os.Open(filepath.Join(c.attempt, artisan.EventsFile))
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []Event
	for s := bufio.NewScanner(f); s.Scan(); {
		var e Event
		if err := json.Unmarshal(s.Bytes(), &e); err != nil {
			c.t.Fatalf("events line %q: %v", s.Text(), err)
		}
		out = append(out, e)
	}
	return out
}

// event returns the first event of kind at rung, failing when there is none.
func (c *launch) event(kind string, rung int) Event {
	c.t.Helper()
	evs := c.events()
	for _, e := range evs {
		if e.Event == kind && e.Rung == rung {
			return e
		}
	}
	c.t.Errorf("no %s event for rung %d in %+v", kind, rung, evs)
	return Event{}
}

func (c *launch) count(kind string, want int) {
	c.t.Helper()
	n := 0
	for _, e := range c.events() {
		if e.Event == kind {
			n++
		}
	}
	if n != want {
		c.t.Errorf("%d %s events, want %d", n, kind, want)
	}
}

// countingUnit counts the rungs the launcher takes; a stuck unit takes them
// without effect, so its members survive every rung.
type countingUnit struct {
	artisan.Unit
	mu    sync.Mutex
	n     map[string]int
	stuck bool
}

func (u *countingUnit) take(rung string, do func() error) error {
	u.mu.Lock()
	u.n[rung]++
	stuck := u.stuck
	u.mu.Unlock()
	if stuck {
		return nil
	}
	return do()
}

func (u *countingUnit) calls(rung string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.n[rung]
}

func (u *countingUnit) Interrupt() error { return u.take("interrupt", u.Unit.Interrupt) }
func (u *countingUnit) Terminate() error { return u.take("terminate", u.Unit.Terminate) }
func (u *countingUnit) Kill() error      { return u.take("kill", u.Unit.Kill) }
