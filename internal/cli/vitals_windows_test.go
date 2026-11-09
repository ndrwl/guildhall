//go:build windows

package cli

import (
	"path/filepath"
	"testing"
)

// TestWindowsVitals: Windows gives memory and disk but no load, which stays unavailable; a directory that does not
// exist gives no disk.
func TestWindowsVitals(t *testing.T) {
	v := machineVitals(t.TempDir())
	if v.LoadOK {
		t.Errorf("load on Windows = %v", v.Load)
	}
	if !v.MemOK || v.MemUsed > v.MemTotal || !v.DiskOK || v.DiskFree > v.DiskTotal || v.Cores < 1 {
		t.Errorf("vitals = %+v", v)
	}
	if v := machineVitals(filepath.Join(t.TempDir(), "missing")); v.DiskOK {
		t.Errorf("missing directory: %+v", v)
	}
}
