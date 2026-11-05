//go:build windows

package workbench

import (
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// isTerminal reports whether f is a console.
func isTerminal(f *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}

// terminalSize is the width in columns and height in rows of f's console window, zero when unknown.
func terminalSize(f *os.File) (cols, rows int) {
	var info windows.ConsoleScreenBufferInfo
	if windows.GetConsoleScreenBufferInfo(windows.Handle(f.Fd()), &info) != nil {
		return 0, 0
	}
	return int(info.Window.Right-info.Window.Left) + 1, int(info.Window.Bottom-info.Window.Top) + 1
}

// enableColor enables virtual terminal processing on f's console, reporting whether it is on: a console that
// cannot enable it, such as an old conhost, shows plain text.
func enableColor(f *os.File) bool {
	h := windows.Handle(f.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}

// watchResize checks f's console size every second, as Windows has no resize signal, and signals when it changed.
func watchResize(f *os.File) (<-chan struct{}, func()) {
	out := make(chan struct{}, 1)
	quit := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		w, h := terminalSize(f)
		for {
			select {
			case <-t.C:
				if nw, nh := terminalSize(f); nw != w || nh != h {
					w, h = nw, nh
					select {
					case out <- struct{}{}:
					default:
					}
				}
			case <-quit:
				return
			}
		}
	}()
	return out, func() { close(quit) }
}
