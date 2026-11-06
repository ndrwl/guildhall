//go:build darwin

package cli

import (
	"encoding/binary"

	"golang.org/x/sys/unix"

	"github.com/ndrwl/guildhall/internal/web"
)

// osVitals reads macOS's vitals with no cgo: the load from sysctl vm.loadavg, memory used from hw.memsize less the
// free pages (vm.page_free_count of vm.pagesize bytes) and the free disk of the volume holding dir from statfs.
func osVitals(dir string) web.Vitals {
	var v web.Vitals
	if b, err := unix.SysctlRaw("vm.loadavg"); err == nil {
		v.Load, v.LoadOK = parseLoadavgRaw(b)
	}
	total, errTotal := unix.SysctlUint64("hw.memsize")
	free, errFree := unix.SysctlUint32("vm.page_free_count")
	page, errPage := unix.SysctlUint32("vm.pagesize")
	if errTotal == nil && errFree == nil && errPage == nil {
		v.MemUsed, v.MemTotal, v.MemOK = memUsed(total, uint64(free)*uint64(page))
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(dir, &fs); err == nil && fs.Bsize > 0 && fs.Blocks > 0 {
		size := uint64(fs.Bsize)
		v.DiskFree, v.DiskTotal, v.DiskOK = fs.Bavail*size, fs.Blocks*size, true
	}
	return v
}

// parseLoadavgRaw is the one-minute load of vm.loadavg's struct loadavg, as 64-bit macOS lays it out little-endian:
// three uint32 fixed-point loads, padding, then the int64 scale they are fixed to.
func parseLoadavgRaw(b []byte) (float64, bool) {
	if len(b) < 24 {
		return 0, false
	}
	load := binary.LittleEndian.Uint32(b[0:4])
	scale := binary.LittleEndian.Uint64(b[16:24])
	if scale == 0 {
		return 0, false
	}
	return float64(load) / float64(scale), true
}

// memUsed is total less free, refused when free exceeds total.
func memUsed(total, free uint64) (used, all uint64, ok bool) {
	if total == 0 || free > total {
		return 0, 0, false
	}
	return total - free, total, true
}
