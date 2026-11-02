//go:build windows && sing && xray && hysteria2

package limiter_test

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var stressMemoryInfo = windows.NewLazySystemDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

func stressRSS() uint64 {
	var counters struct {
		Size, PageFaults                     uint32
		PeakWorkingSet, WorkingSet           uintptr
		PeakPagedPool, PagedPool             uintptr
		PeakNonpagedPool, NonpagedPool       uintptr
		Pagefile, PeakPagefile, PrivateUsage uintptr
	}
	counters.Size = uint32(unsafe.Sizeof(counters))
	result, _, _ := stressMemoryInfo.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&counters)), uintptr(counters.Size))
	if result == 0 {
		return 0
	}
	return uint64(counters.WorkingSet)
}
