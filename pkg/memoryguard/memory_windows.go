//go:build windows

package memoryguard

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

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

var globalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

func Available() (uint64, error) {
	var status memoryStatusEx
	status.Length = uint32(unsafe.Sizeof(status))
	result, _, callErr := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if result == 0 {
		return 0, fmt.Errorf("GlobalMemoryStatusEx: %w", callErr)
	}
	return status.AvailPhys, nil
}
