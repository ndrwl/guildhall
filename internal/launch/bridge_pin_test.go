package launch

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/workbench"
)

// pinned is a coloured 60×24 terminal pane on clock.
func pinned(clock func() time.Time) pane {
	return pane{Opts: workbench.Options{Width: 60, Height: 24, Color: true, Location: time.UTC}, Now: clock}
}

const unpin = "\x1b7\x1b[r\x1b8"

// TestPresentWorkbenchPinsTheBar: on a coloured terminal the bar is pinned above a scroll region, redrawn once a
// second, redrawn at a resize, and the region is reset before the banner, so the pane ends an ordinary terminal.
func TestPresentWorkbenchPinsTheBar(t *testing.T) {
	at := bridgeAttempt(t, bridgeLog, bridgeEvents(), adapter.StatusSucceeded, "Built it.")
	start := time.Date(2026, 10, 31, 9, 10, 0, 0, time.UTC)
	tick := start
	clock := func() time.Time { tick = tick.Add(700 * time.Millisecond); return tick }
	done := make(chan error, 1)
	resized := make(chan struct{}, 1)
	resized <- struct{}{}
	p := pinned(clock)
	p.Resized = resized
	// The launcher exits once the resize has been taken.
	p.Size = func() (int, int) { done <- nil; return 50, 20 }
	var out bytes.Buffer
	b := Bridge{Attempt: at, URL: "http://h:1/guildhall/d36", Start: start}
	if err := presentWorkbench(b, filepath.Join(at.Dir(), LogFile), done, &out, p); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"\x1b[H\x1b[2J\x1b[1;1H\x1b[2K", // set-up
		"\x1b[5;24r\x1b[5;1H",           // the region below a four-row bar
		"\x1b7\x1b[1;1H\x1b[2K",         // a redraw
		"\x1b7\x1b[r\x1b[1;1H\x1b[2K",   // the resize
		"\x1b[3;20r\x1b8",               // the narrower bar's region
		unpin + "\n\x1b[0;2m──────────", // the reset before the banner
	} {
		if !strings.Contains(got, want) {
			t.Errorf("pinned workbench lacks %q:\n%q", want, got)
		}
	}
	if strings.Count(got, "\x1b[r") != 2 || strings.LastIndex(got, "\x1b[r") > strings.Index(got, "✓ succeeded") {
		t.Errorf("the region is not reset exactly at the resize and before the banner:\n%q", got)
	}
}

// TestPresentWorkbenchStopSignal: a stop signal resets the region and ends the presentation with an error naming
// it, without a banner, leaving the launcher running.
func TestPresentWorkbenchStopSignal(t *testing.T) {
	at := bridgeAttempt(t, bridgeLog, bridgeEvents(), "", "")
	p := pinned(time.Now)
	stop := make(chan os.Signal, 1)
	stop <- syscall.SIGTERM
	p.Stop = stop
	var out bytes.Buffer
	err := presentWorkbench(Bridge{Attempt: at}, filepath.Join(at.Dir(), LogFile), make(chan error), &out, p)
	if err == nil || !strings.Contains(err.Error(), "terminated") {
		t.Errorf("err = %v, want one naming the signal", err)
	}
	if !strings.HasSuffix(out.String(), unpin+"\x1b[0m") || strings.Contains(out.String(), "no result written") {
		t.Errorf("stopped workbench does not end reset, without a banner: %q", out.String())
	}
}

// failingPane fails every write after the first, recording each one it was asked to make.
type failingPane struct{ writes []string }

func (w *failingPane) Write(p []byte) (int, error) {
	w.writes = append(w.writes, string(p))
	if len(w.writes) > 1 {
		return 0, errors.New("pane gone")
	}
	return len(p), nil
}

// TestPresentWorkbenchOutputError: an output error ends the presentation, and the region is still reset.
func TestPresentWorkbenchOutputError(t *testing.T) {
	at := bridgeAttempt(t, bridgeLog, bridgeEvents(), "", "")
	w := &failingPane{}
	if err := presentWorkbench(Bridge{Attempt: at}, filepath.Join(at.Dir(), LogFile), make(chan error), w, pinned(time.Now)); err == nil {
		t.Fatal("no output error")
	}
	if last := w.writes[len(w.writes)-1]; !strings.Contains(w.writes[0], "\x1b[4;24r") || last != unpin {
		t.Errorf("writes = %q, want the pinned bar first and the reset last", w.writes)
	}
}
