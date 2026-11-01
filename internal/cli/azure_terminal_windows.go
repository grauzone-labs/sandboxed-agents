package cli

import "golang.org/x/sys/windows"

// snapshotAzureTerminal remembers console modes so cancelling Podman cannot leave the host raw.
func snapshotAzureTerminal(streams ...any) func() {
	var restores []func()
	for _, stream := range streams {
		file, ok := stream.(interface{ Fd() uintptr })
		if !ok {
			continue
		}
		handle := windows.Handle(file.Fd())
		var previous uint32
		if windows.GetConsoleMode(handle, &previous) == nil {
			restores = append(restores, func() { windows.SetConsoleMode(handle, previous) })
		}
	}
	return func() {
		for _, restore := range restores {
			restore()
		}
	}
}
