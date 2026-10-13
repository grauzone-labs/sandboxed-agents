package cli

import (
	"fmt"
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

// windowsNativeArchitecture uses IsWow64Process2 to report the true native
// machine even when an AMD64 process runs under ARM64 emulation.
func windowsNativeArchitecture() (uint16, error) {
	procedure := syscall.NewLazyDLL("kernel32.dll").NewProc("IsWow64Process2")
	if err := procedure.Find(); err != nil {
		return 0, err
	}
	var processMachine, nativeMachine uint16
	// The process identifier can be AMD64 under ARM64 emulation; the native PE
	// identifier is authoritative. GetNativeSystemInfo can report the emulated ISA.
	result, _, callErr := procedure.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&processMachine)), uintptr(unsafe.Pointer(&nativeMachine)))
	if result == 0 {
		if callErr != nil && callErr != syscall.Errno(0) {
			return 0, callErr
		}
		return 0, fmt.Errorf("IsWow64Process2 query failed")
	}
	return nativeMachine, nil
}
