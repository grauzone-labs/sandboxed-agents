//go:build !linux && !windows

package cli

// snapshotAzureTerminal leaves unsupported host terminal handling to its process runner.
func snapshotAzureTerminal(streams ...any) func() { return func() {} }
