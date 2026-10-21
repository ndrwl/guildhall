package claude

import (
	"errors"
	"os"
)

// stop terminates Claude: Windows has no interrupt to send one process.
func stop(p *os.Process) error {
	if err := p.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
