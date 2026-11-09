//go:build darwin

package cli

import (
	"encoding/binary"
	"testing"
)

// TestDarwinVitals: the struct loadavg's first load over its scale is the load, a short or unscaled one is
// unavailable; memory used is the total less the free pages; and this Mac answers with its load, memory and disk.
func TestDarwinVitals(t *testing.T) {
	b := make([]byte, 24)
	binary.LittleEndian.PutUint32(b[0:], 3113) // 1.52 at a scale of 2048
	binary.LittleEndian.PutUint64(b[16:], 2048)
	if l, ok := parseLoadavgRaw(b); !ok || l < 1.51 || l > 1.53 {
		t.Errorf("load = %v, %v", l, ok)
	}
	if _, ok := parseLoadavgRaw(b[:16]); ok {
		t.Error("a short loadavg reads")
	}
	if _, ok := parseLoadavgRaw(make([]byte, 24)); ok {
		t.Error("an unscaled loadavg reads")
	}
	if used, total, ok := memUsed(16, 4); !ok || used != 12 || total != 16 {
		t.Errorf("memUsed = %d, %d, %v", used, total, ok)
	}
	if _, _, ok := memUsed(4, 16); ok {
		t.Error("more free than total reads")
	}
	if v := machineVitals(t.TempDir()); !v.LoadOK || !v.MemOK || !v.DiskOK || v.Cores < 1 {
		t.Errorf("vitals = %+v", v)
	}
}
