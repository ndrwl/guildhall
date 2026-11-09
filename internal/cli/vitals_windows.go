//go:build windows

package cli

import (
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ndrwl/guildhall/internal/web"
)

// globalMemoryStatusEx is kernel32's GlobalMemoryStatusEx, which golang.org/x/sys/windows does not wrap.
var globalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

// memoryStatusEx is Windows's MEMORYSTATUSEX.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// osVitals reads Windows's vitals with no cgo: memory from GlobalMemoryStatusEx and the free disk of the volume
// holding dir from GetDiskFreeSpaceEx. Windows has no load average, so the load stays unavailable.
func osVitals(dir string) web.Vitals {
	var v web.Vitals
	if m, ok := memoryStatus(); ok && m.TotalPhys > 0 && m.AvailPhys <= m.TotalPhys {
		v.MemUsed, v.MemTotal, v.MemOK = m.TotalPhys-m.AvailPhys, m.TotalPhys, true
	}
	if p, err := windows.UTF16PtrFromString(dir); err == nil {
		var free, total, totalFree uint64
		if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err == nil && total > 0 {
			v.DiskFree, v.DiskTotal, v.DiskOK = free, total, true
		}
	}
	return v
}

// memoryStatus calls GlobalMemoryStatusEx, reporting whether it answered.
func memoryStatus() (memoryStatusEx, bool) {
	m := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if globalMemoryStatusEx.Find() != nil {
		return m, false
	}
	r, _, _ := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	return m, r != 0
}
