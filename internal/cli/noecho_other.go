//go:build !(linux || darwin)

package cli

import (
	"errors"
	"os"
)

// noEcho cannot turn echo off on this platform: a terminal is refused, and the value is piped on standard input.
func noEcho(f *os.File) (terminal bool, restore func(), err error) {
	if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return true, func() {}, errors.New("a value cannot be typed without echo here; pipe it on standard input")
	}
	return false, func() {}, nil
}
