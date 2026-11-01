//go:build unix

package workbench

import (
	"os"

	"golang.org/x/sys/unix"
)

// isTerminal reports whether f is a terminal: one that answers a window size request.
func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	return err == nil
}

// terminalWidth is f's width in columns, or zero when unknown.
func terminalWidth(f *os.File) int {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0
	}
	return int(ws.Col)
}

// enableColor reports whether f can show SGR codes: any Unix terminal can.
func enableColor(*os.File) bool { return true }
