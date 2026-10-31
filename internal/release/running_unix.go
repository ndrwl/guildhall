//go:build !windows

package release

// RunningFrom is, on Windows, the processes other than self running the executable at path, which a rename could
// not replace; elsewhere a running executable's file can be replaced, so it is none.
func RunningFrom(path string, self int) ([]int, error) { return nil, nil }
