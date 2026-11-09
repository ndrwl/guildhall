//go:build linux

package cli

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/ndrwl/guildhall/internal/web"
)

// procRoot is where Linux's /proc is read from; a test points it at a fake one.
var procRoot = "/proc"

// osVitals reads Linux's vitals: the load from /proc/loadavg, memory used from /proc/meminfo (MemTotal less
// MemAvailable) and the free disk of the volume holding dir from statfs.
func osVitals(dir string) web.Vitals {
	var v web.Vitals
	if b, err := os.ReadFile(filepath.Join(procRoot, "loadavg")); err == nil {
		v.Load, v.LoadOK = parseLoadavg(b)
	}
	if b, err := os.ReadFile(filepath.Join(procRoot, "meminfo")); err == nil {
		v.MemUsed, v.MemTotal, v.MemOK = parseMeminfo(b)
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(dir, &fs); err == nil {
		size := uint64(fs.Frsize) //nolint:gosec // a block size is positive
		if fs.Frsize <= 0 {
			size = uint64(fs.Bsize) //nolint:gosec // a block size is positive
		}
		if size > 0 && fs.Blocks > 0 {
			v.DiskFree, v.DiskTotal, v.DiskOK = fs.Bavail*size, fs.Blocks*size, true
		}
	}
	return v
}

// parseLoadavg is /proc/loadavg's one-minute load average.
func parseLoadavg(b []byte) (float64, bool) {
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, false
	}
	x, err := strconv.ParseFloat(f[0], 64)
	if err != nil || x < 0 {
		return 0, false
	}
	return x, true
}

// parseMeminfo is /proc/meminfo's memory used and total in bytes: MemTotal less MemAvailable, or, on a kernel
// before MemAvailable, less MemFree, Buffers and Cached.
func parseMeminfo(b []byte) (used, total uint64, ok bool) {
	kb := map[string]uint64{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		key, rest, found := strings.Cut(sc.Text(), ":")
		if !found {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil || (len(f) > 1 && f[1] != "kB") {
			continue
		}
		kb[key] = n
	}
	total, hasTotal := kb["MemTotal"]
	avail, hasAvail := kb["MemAvailable"]
	if !hasAvail {
		free, hasFree := kb["MemFree"]
		avail, hasAvail = free+kb["Buffers"]+kb["Cached"], hasFree
	}
	if !hasTotal || !hasAvail || total == 0 || avail > total {
		return 0, 0, false
	}
	return (total - avail) * 1024, total * 1024, true
}
