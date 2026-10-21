package fake

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/atomicfile"
)

// This file holds the P0 lifecycle directives: the process tree, the cancel
// marker, interrupts, interaction and the inventory (docs/adapters.md).

// markerPoll is how often a fake looks for the cancel marker.
const markerPoll = 20 * time.Millisecond

// errMarked ends a fake's work when the attempt's cancel marker appears.
var errMarked = errors.New("cancel marker present")

// handleInterrupt ends ctx on an interrupt (SIGINT, or CTRL_C and CTRL_BREAK
// on Windows), or with ignore-interrupt handles and discards it, so the
// process keeps working. The returned function restores the default.
func handleInterrupt(ctx context.Context, d directives) (context.Context, func()) {
	if !d.ignoreInterrupt {
		return signal.NotifyContext(ctx, os.Interrupt)
	}
	// Notify and discard rather than ignore: a handled console event keeps a
	// Windows process alive.
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	return ctx, func() { signal.Stop(c) }
}

// watchMarker returns ctx ended with cause errMarked once the attempt's
// cancel marker is present, when watch is set.
func watchMarker(ctx context.Context, a Args, watch bool) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(ctx)
	if watch {
		go func() {
			t := time.NewTicker(markerPoll)
			defer t.Stop()
			for {
				if marked, _ := adapter.Cancelled(a.ResultFile, a.Identity); marked {
					cancel(errMarked)
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
	return ctx, func() { cancel(nil) }
}

// grandchild is a process the worker started: it records itself and works
// until the cancel marker (unless ignore-cancel), an interrupt (unless
// ignore-interrupt) or its lifetime ends it, and never writes a result.
func grandchild(ctx context.Context, a Args, d directives, sleep func(context.Context, time.Duration) error) error {
	ctx, stop := handleInterrupt(ctx, d)
	defer stop()
	if err := recordSelf(d.inventory, RoleGrandchild); err != nil {
		return err
	}
	ctx, cancel := watchMarker(ctx, a, !d.ignoreCancel)
	defer cancel()
	if err := sleep(ctx, d.lifetime); err != nil && !errors.Is(context.Cause(ctx), errMarked) {
		return err
	}
	return nil
}

// startTree records the worker and starts its grandchildren: fake
// grandchildren, which record themselves, or native commands, which the
// worker records.
func startTree(a Args, d directives) error {
	if err := recordSelf(d.inventory, RoleWorker); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	for range d.grandchildren {
		var c *exec.Cmd
		if d.native {
			c = nativeCommand(d.lifetime)
		} else {
			c = exec.Command(exe, Subcommand, "--role", RoleGrandchild, "--task", a.TaskID, "--attempt", a.AttemptID,
				"--prompt", a.PromptFile, "--result", a.ResultFile)
		}
		if err := c.Start(); err != nil {
			return fmt.Errorf("start grandchild: %w", err)
		}
		go func() { _ = c.Wait() }()
		if d.native && d.inventory != "" {
			id, err := artisan.Lookup(c.Process.Pid)
			if err == nil {
				err = record(d.inventory, RoleGrandchild, id)
			}
			if err != nil {
				return fmt.Errorf("record grandchild: %w", err)
			}
		}
	}
	return nil
}

// recordSelf records this process in the inventory directory dir, if set.
func recordSelf(dir, role string) error {
	if dir == "" {
		return nil
	}
	self, err := artisan.Self()
	if err != nil {
		return err
	}
	return record(dir, role, self)
}

// record writes one inventory entry, <role>-<pid>.json holding the
// identity and role, as artisantest.Inventory reads it.
func record(dir, role string, id artisan.Identity) error {
	b, err := json.Marshal(struct {
		artisan.Identity
		Role string `json:"role"`
	}{id, role})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, fmt.Sprintf("%s-%d.json", role, id.PID)), b)
}

// publishLate publishes r although the attempt is cancelled, as an Artisan
// that ignores the marker would: the adapter refuses to write it beside the
// marker, so it is written aside and linked into place, once.
func publishLate(a Args, r adapter.Result) error {
	dir, err := os.MkdirTemp(filepath.Dir(a.ResultFile), ".late-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	aside := filepath.Join(dir, filepath.Base(a.ResultFile))
	if err := adapter.WriteResult(aside, r); err != nil {
		return err
	}
	return os.Link(aside, a.ResultFile)
}

// echo copies input lines to out, so a pane shows what was typed.
func echo(in io.Reader, out io.Writer) {
	for s := bufio.NewScanner(in); s.Scan(); {
		_, _ = fmt.Fprintf(out, "fake artisan: echo: %s\n", s.Text())
	}
}
