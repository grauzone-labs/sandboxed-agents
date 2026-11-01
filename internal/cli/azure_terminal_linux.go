package cli

import "golang.org/x/sys/unix"

// snapshotAzureTerminal remembers real terminal modes so cancelling Podman cannot leave the host raw.
func snapshotAzureTerminal(streams ...any) func() {
	var restores []func()
	for _, stream := range streams {
		file, ok := stream.(interface{ Fd() uintptr })
		if !ok {
			continue
		}
		fd := int(file.Fd())
		previous, err := unix.IoctlGetTermios(fd, unix.TCGETS)
		if err == nil {
			restores = append(restores, func() { unix.IoctlSetTermios(fd, unix.TCSETS, previous) })
		}
	}
	return func() {
		for _, restore := range restores {
			restore()
		}
	}
}
