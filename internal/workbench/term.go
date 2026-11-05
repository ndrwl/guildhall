package workbench

import (
	"io"
	"os"
	"strconv"
)

// Output modes of an Artisan pane, the values of the artisan_pane_output setting.
const (
	// OutputWorkbench renders the Workbench, the default.
	OutputWorkbench = "workbench"
	// OutputPlain copies launcher.log to the pane unchanged, as before the Workbench.
	OutputPlain = "plain"
)

// DefaultWidth is the width of a pane whose size is unknown; MinWidth is the narrowest one rendered.
const (
	DefaultWidth = 80
	MinWidth     = 40
)

// Detect returns the Options for out given the environment getenv reads. Colour is on only when out is a terminal,
// NO_COLOR is unset or empty (no-color.org), TERM is not dumb, and, on Windows, virtual terminal processing can be
// enabled. The width is the terminal's, else COLUMNS, else DefaultWidth, and at least MinWidth; the height is the
// terminal's, zero when out is not one.
func Detect(out io.Writer, getenv func(string) string) Options {
	f, _ := out.(*os.File)
	tty := f != nil && isTerminal(f)
	opts := Options{Width: 0}
	if tty {
		opts.Width, opts.Height = terminalSize(f)
	}
	if opts.Width <= 0 {
		if n, err := strconv.Atoi(getenv("COLUMNS")); err == nil && n > 0 {
			opts.Width = n
		}
	}
	if opts.Width <= 0 {
		opts.Width = DefaultWidth
	}
	opts.Width = max(opts.Width, MinWidth)
	opts.Color = tty && getenv("NO_COLOR") == "" && getenv("TERM") != "dumb" && enableColor(f)
	return opts
}

// Size is out's width and height when it is a terminal, zero when it is not or the size is unknown.
func Size(out io.Writer) (cols, rows int) {
	f, _ := out.(*os.File)
	if f == nil || !isTerminal(f) {
		return 0, 0
	}
	return terminalSize(f)
}

// WatchResize returns a channel that receives when out, a terminal, may have been resized, on SIGWINCH on Unix
// and from a check every second on Windows, and a function that stops watching. A writer that is not a terminal
// gets a channel that never receives.
func WatchResize(out io.Writer) (<-chan struct{}, func()) {
	f, _ := out.(*os.File)
	if f == nil || !isTerminal(f) {
		return nil, func() {}
	}
	return watchResize(f)
}
