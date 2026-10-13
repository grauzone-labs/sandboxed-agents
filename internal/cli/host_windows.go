package cli

import "golang.org/x/sys/windows"

func windowsBuild() (uint32, error) { return windows.RtlGetVersion().BuildNumber, nil }

func windowsWorkstation() (bool, error) { return windows.RtlGetVersion().ProductType == 1, nil }
