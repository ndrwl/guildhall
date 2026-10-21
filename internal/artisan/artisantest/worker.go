package artisantest

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/atomicfile"
)

// workerEnv carries a Worker to the test binary playing it.
const workerEnv = "GUILDHALL_UNIT_WORKER"

// Worker is the process tree the suite asks a unit to contain. The test binary
// plays every process in it; each records its own identity in the inventory.
type Worker struct {
	// Grandchildren is how many children the worker starts.
	Grandchildren int `json:"grandchildren,omitempty"`
	// IgnoreInterrupt makes every process ignore SIGINT and CTRL_BREAK.
	IgnoreInterrupt bool `json:"ignore_interrupt,omitempty"`
	// SetsidGrandchild starts grandchildren in a new session (Unix only).
	SetsidGrandchild bool `json:"setsid_grandchild,omitempty"`
	// BreakawayGrandchild first asks for CREATE_BREAKAWAY_FROM_JOB (Windows
	// only) and records the answer; a refused grandchild is started normally.
	BreakawayGrandchild bool `json:"breakaway_grandchild,omitempty"`
	// ExitAfterSpawn makes the worker exit once its grandchildren are
	// started, orphaning them.
	ExitAfterSpawn bool `json:"exit_after_spawn,omitempty"`
	// Lifetime bounds every process; zero means one minute.
	Lifetime time.Duration `json:"lifetime,omitempty"`
	Release  string        `json:"release,omitempty"`
	// CancelMarker makes every process honour the cancel marker: it exits
	// once that file exists.
	CancelMarker string `json:"cancel_marker,omitempty"`
	// Result makes the worker publish ResultBody, or ResultBody the constant
	// when that is empty, at that path once ResultAfter has elapsed and
	// ResultMarker, if set, exists, then exit. Release and CancelMarker take
	// precedence over publication.
	Result       string        `json:"result,omitempty"`
	ResultBody   string        `json:"result_body,omitempty"`
	ResultAfter  time.Duration `json:"result_after,omitempty"`
	ResultMarker string        `json:"result_marker,omitempty"`

	Role      string `json:"role,omitempty"`
	Inventory string `json:"inventory,omitempty"`
}

// Entry is one inventory record: a process the worker tree started.
type Entry struct {
	artisan.Identity
	// Role is "worker", "grandchild", "sentinel" or "breakaway" (the
	// worker's record of a breakaway request).
	Role string `json:"role"`
	// Setsid is set on a grandchild started in its own session.
	Setsid bool `json:"setsid,omitempty"`
	// Breakaway is "refused: <error>" or "succeeded" on a breakaway record.
	Breakaway string `json:"breakaway,omitempty"`
}

// Command returns the command that runs w as the worker, recording into dir.
// Its stdin, stdout and stderr are left unset.
func Command(dir string, w Worker) *exec.Cmd {
	w.Role, w.Inventory = "worker", dir
	return w.command()
}

func (w Worker) command() *exec.Cmd {
	b, _ := json.Marshal(w)
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), workerEnv+"="+string(b))
	return cmd
}

// WorkerOf returns the Worker a Command built, so a test double can model it.
func WorkerOf(cmd *exec.Cmd) (Worker, bool) {
	var w Worker
	for _, kv := range cmd.Env {
		if v, ok := strings.CutPrefix(kv, workerEnv+"="); ok && json.Unmarshal([]byte(v), &w) == nil {
			return w, true
		}
	}
	return w, false
}

// Record writes e into the inventory directory dir.
func Record(dir string, e Entry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, fmt.Sprintf("%s-%d.json", e.Role, e.PID)), b)
}

// Inventory reads every entry recorded in dir.
func Inventory(dir string) ([]Entry, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, n := range names {
		b, err := atomicfile.Read(n)
		if err != nil {
			return nil, err
		}
		var e Entry
		if err := json.Unmarshal(b, &e); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		out = append(out, e)
	}
	return out, nil
}

// MainIfWorker runs the worker and exits when the test binary was started by
// Command; otherwise it returns. Call it first in TestMain.
func MainIfWorker() {
	v, ok := os.LookupEnv(workerEnv)
	if !ok {
		return
	}
	var w Worker
	if err := json.Unmarshal([]byte(v), &w); err != nil {
		fmt.Fprintln(os.Stderr, "artisantest worker:", err)
		os.Exit(2)
	}
	os.Exit(w.run())
}

func (w Worker) run() int {
	if w.IgnoreInterrupt {
		// Notify and discard rather than ignore: a handled console event keeps
		// a Windows process alive.
		c := make(chan os.Signal, 8)
		signal.Notify(c, os.Interrupt)
		go func() {
			for range c {
			}
		}()
	}
	self, err := artisan.Self()
	if err != nil {
		return 2
	}
	if err := Record(w.Inventory, Entry{Identity: self, Role: w.Role, Setsid: w.Role == "grandchild" && w.SetsidGrandchild && setsidAvailable}); err != nil {
		return 2
	}
	if w.Role == "worker" {
		for range w.Grandchildren {
			if err := w.spawnGrandchild(); err != nil {
				fmt.Fprintln(os.Stderr, "artisantest worker:", err)
				return 2
			}
		}
		if w.ExitAfterSpawn {
			return 0
		}
	}
	w.Wait()
	return 0
}

// Wait returns when the release file or the honoured cancel marker exists,
// once the result is published, or at the end of the lifetime.
func (w Worker) Wait() {
	start := time.Now()
	lifetime := w.Lifetime
	if lifetime == 0 {
		lifetime = time.Minute
	}
	release := w.Release
	if release == "" {
		release = filepath.Join(w.Inventory, "release")
	}
	for end := time.Now().Add(lifetime); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(release); err == nil {
			return
		}
		if _, err := os.Stat(w.CancelMarker); w.CancelMarker != "" && err == nil {
			return
		}
		if w.Result != "" && time.Since(start) >= w.ResultAfter {
			if w.ResultMarker != "" {
				if _, err := os.Stat(w.ResultMarker); err != nil {
					continue
				}
			}
			body := w.ResultBody
			if body == "" {
				body = ResultBody
			}
			_ = atomicfile.Write(w.Result, []byte(body))
			return
		}
	}
}

func (w Worker) spawnGrandchild() error {
	g := w
	g.Role, g.Grandchildren, g.ExitAfterSpawn, g.Result = "grandchild", 0, false, ""
	if w.BreakawayGrandchild && breakawayAvailable {
		c := g.command()
		applyBreakaway(c)
		e := Entry{Role: "breakaway", Breakaway: "succeeded"}
		if err := c.Start(); err != nil {
			e.Breakaway = "refused: " + err.Error()
		} else {
			go func() { _ = c.Wait() }()
		}
		e.Identity = artisan.Identity{PID: os.Getpid()}
		if c.Process != nil {
			e.PID = c.Process.Pid
		}
		if err := Record(w.Inventory, e); err != nil || e.Breakaway == "succeeded" {
			return err
		}
	}
	c := g.command()
	if w.SetsidGrandchild {
		applySetsid(c)
	}
	if err := c.Start(); err != nil {
		return err
	}
	go func() { _ = c.Wait() }()
	return nil
}
