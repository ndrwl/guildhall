//go:build windows

package workbench

import (
	"os"

	"golang.org/x/sys/windows"
)

// isTerminal reports whether f is a console.
func isTerminal(f *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}

// terminalWidth is the width of f's console window in columns, or zero when unknown.
func terminalWidth(f *os.File) int {
	var info windows.ConsoleScreenBufferInfo
	if windows.GetConsoleScreenBufferInfo(windows.Handle(f.Fd()), &info) != nil {
		return 0
	}
	return int(info.Window.Right-info.Window.Left) + 1
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
