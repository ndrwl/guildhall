//go:build unix

package claude

import (
	"errors"
	"os"
)

// stop asks Claude to stop as Ctrl+C would.
func stop(p *os.Process) error {
	if err := p.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
