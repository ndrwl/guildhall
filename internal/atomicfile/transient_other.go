//go:build !windows

package atomicfile

// transient reports no error: a rename here never races an open.
func transient(error) bool { return false }
