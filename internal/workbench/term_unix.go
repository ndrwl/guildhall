//go:build unix

package workbench

import (
	"os"
	"os/signal"

	"golang.org/x/sys/unix"
)

// isTerminal reports whether f is a terminal: one that answers a window size request.
func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	return err == nil
}

// terminalSize is f's width in columns and height in rows, zero when unknown.
func terminalSize(f *os.File) (cols, rows int) {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0
	}
	return int(ws.Col), int(ws.Row)
}

// enableColor reports whether f can show SGR codes: any Unix terminal can.
func enableColor(*os.File) bool { return true }

// watchResize signals on SIGWINCH, which the kernel sends when the controlling terminal is resized.
func watchResize(*os.File) (<-chan struct{}, func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, unix.SIGWINCH)
	out := make(chan struct{}, 1)
	quit := make(chan struct{})
	go func() {
		for {
			select {
			case <-sig:
				select {
				case out <- struct{}{}:
				default:
				}
			case <-quit:
				return
			}
		}
	}()
	return out, func() { signal.Stop(sig); close(quit) }
}
