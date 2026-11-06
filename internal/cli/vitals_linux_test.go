//go:build linux

package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLinuxVitals: over a fake /proc the load is /proc/loadavg's first field and memory used MemTotal less
// MemAvailable; the disk is statfs's of the directory given; a missing or garbled /proc leaves load and memory
// unavailable, and a directory statfs cannot read leaves the disk so.
func TestLinuxVitals(t *testing.T) {
	proc := t.TempDir()
	writeFile(t, filepath.Join(proc, "loadavg"), "1.52 0.98 0.71 2/1234 5678\n")
	writeFile(t, filepath.Join(proc, "meminfo"), "MemTotal:       16000000 kB\nMemFree:         1000000 kB\nMemAvailable:    4000000 kB\nBuffers:          200000 kB\nHugePages_Total:       0\n")
	old := procRoot
	procRoot = proc
	defer func() { procRoot = old }()

	v := machineVitals(t.TempDir())
	if !v.LoadOK || v.Load != 1.52 {
		t.Errorf("load = %v, %v", v.Load, v.LoadOK)
	}
	if !v.MemOK || v.MemTotal != 16_000_000*1024 || v.MemUsed != 12_000_000*1024 {
		t.Errorf("memory = %d of %d, %v", v.MemUsed, v.MemTotal, v.MemOK)
	}
	if !v.DiskOK || v.DiskTotal == 0 || v.DiskFree > v.DiskTotal {
		t.Errorf("disk = %d of %d, %v", v.DiskFree, v.DiskTotal, v.DiskOK)
	}
	if v.Cores < 1 {
		t.Errorf("cores = %d", v.Cores)
	}

	// an older kernel's meminfo has no MemAvailable: free, buffers and cache are available
	writeFile(t, filepath.Join(proc, "meminfo"), "MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 50 kB\nCached: 250 kB\n")
	if v := machineVitals(t.TempDir()); !v.MemOK || v.MemUsed != 600*1024 {
		t.Errorf("no MemAvailable: memory = %d, %v", v.MemUsed, v.MemOK)
	}

	writeFile(t, filepath.Join(proc, "loadavg"), "garbled\n")
	writeFile(t, filepath.Join(proc, "meminfo"), "MemTotal: 1000 kB\nMemAvailable: 2000 kB\n")
	if v := machineVitals(filepath.Join(t.TempDir(), "missing")); v.LoadOK || v.MemOK || v.DiskOK {
		t.Errorf("garbled: %+v", v)
	}
	if err := os.Remove(filepath.Join(proc, "loadavg")); err != nil {
		t.Fatal(err)
	}
	if v := machineVitals(t.TempDir()); v.LoadOK {
		t.Errorf("no loadavg: %+v", v)
	}
}
