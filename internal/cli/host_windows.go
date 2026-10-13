package cli

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func windowsBuild() (uint32, error) { return windows.RtlGetVersion().BuildNumber, nil }

// workstationProductType is Windows VER_NT_WORKSTATION, excluding server editions.
const workstationProductType = 1

func windowsWorkstation() (bool, error) {
	return windows.RtlGetVersion().ProductType == workstationProductType, nil
}

// nativeSystemInfo matches SYSTEM_INFO, including its initial WORD union fields
// and pointer-sized address and affinity fields on both 32-bit and 64-bit hosts.
type nativeSystemInfo struct {
	ProcessorArchitecture     uint16
	Reserved                  uint16
	PageSize                  uint32
	MinimumApplicationAddress uintptr
	MaximumApplicationAddress uintptr
	ActiveProcessorMask       uintptr
	NumberOfProcessors        uint32
	ProcessorType             uint32
	AllocationGranularity     uint32
	ProcessorLevel            uint16
	ProcessorRevision         uint16
}

func windowsNativeArchitecture() (uint16, error) {
	procedure := syscall.NewLazyDLL("kernel32.dll").NewProc("GetNativeSystemInfo")
	if err := procedure.Find(); err != nil {
		return 0, err
	}
	var info nativeSystemInfo
	// GetNativeSystemInfo reports the OS architecture even under emulation.
	// It returns void, so Call's last-error value can be stale and is ignored.
	procedure.Call(uintptr(unsafe.Pointer(&info)))
	return info.ProcessorArchitecture, nil
}
