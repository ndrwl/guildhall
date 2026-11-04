//go:build !linux && !darwin && !windows

package setup

import (
	"errors"
	"os"
)

// noEcho cannot turn off echo here, so a secret is read as typed; the platforms Guildhall supports all can.
func noEcho(*os.File) (func(), error) { return nil, errors.New("echo control unsupported") }
