package workbench

import (
	"bytes"
	"os"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// openPTY opens a pseudo-terminal sized cols wide and returns its terminal side.
func openPTY(t *testing.T, cols uint16) *os.File {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	tty, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tty.Close() })
	if err := unix.IoctlSetWinsize(int(tty.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: cols, Row: 24}); err != nil {
		t.Fatal(err)
	}
	return tty
}

// TestDetectTerminal: a terminal gets colour and its own width, unless NO_COLOR is set or TERM is dumb; an empty
// NO_COLOR does not count (no-color.org).
func TestDetectTerminal(t *testing.T) {
	tty := openPTY(t, 132)
	for _, tc := range []struct {
		name  string
		vars  map[string]string
		color bool
	}{
		{"xterm", map[string]string{"TERM": "xterm-256color", "COLUMNS": "50"}, true},
		{"no TERM", map[string]string{}, true},
		{"empty NO_COLOR", map[string]string{"TERM": "xterm", "NO_COLOR": ""}, true},
		{"NO_COLOR", map[string]string{"TERM": "xterm", "NO_COLOR": "1"}, false},
		{"TERM=dumb", map[string]string{"TERM": "dumb"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(tty, env(tc.vars))
			if got.Color != tc.color || got.Width != 132 {
				t.Errorf("Detect = %+v, want colour %v at the terminal's width 132", got, tc.color)
			}
		})
	}
	if got := Detect(tty, env(nil)); got.Height != 24 {
		t.Errorf("Detect height = %d, want the terminal's 24", got.Height)
	}
	narrow := openPTY(t, 30)
	if got := Detect(narrow, env(nil)); got.Width != MinWidth {
		t.Errorf("a 30 column terminal renders at %d, want %d", got.Width, MinWidth)
	}
}

// TestWatchResize: a terminal's size is read and a SIGWINCH is passed on; a writer that is not a terminal has no
// size and its channel never receives.
func TestWatchResize(t *testing.T) {
	tty := openPTY(t, 100)
	if w, h := Size(tty); w != 100 || h != 24 {
		t.Errorf("Size = %d×%d, want 100×24", w, h)
	}
	resized, stop := WatchResize(tty)
	defer stop()
	if err := unix.Kill(os.Getpid(), unix.SIGWINCH); err != nil {
		t.Fatal(err)
	}
	select {
	case <-resized:
	case <-time.After(10 * time.Second):
		t.Fatal("no resize after SIGWINCH")
	}

	var buf bytes.Buffer
	if w, h := Size(&buf); w != 0 || h != 0 {
		t.Errorf("Size of a buffer = %d×%d", w, h)
	}
	none, stopNone := WatchResize(&buf)
	stopNone()
	if none != nil {
		t.Error("a buffer has a resize channel")
	}
}
