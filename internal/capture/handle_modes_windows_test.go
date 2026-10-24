//go:build windows

package capture_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/grauzone-labs/sandboxed-agents/internal/capture"
)

// fileModeInformation is the FILE_INFORMATION_CLASS whose FILE_MODE_INFORMATION reports whether a
// handle completes I/O synchronously.
const fileModeInformation = 16

// handleModes reports whether the fixture's standard output and standard error handles complete
// writes asynchronously, the mode on which the inbox Windows OpenSSH client depends.
func handleModes() string {
	return fmt.Sprintf("stdout=%s stderr=%s", handleMode(windows.Handle(os.Stdout.Fd())), handleMode(windows.Handle(os.Stderr.Fd())))
}

// handleMode names a handle's completion mode: async, sync, or the query's failure.
func handleMode(handle windows.Handle) string {
	var mode uint32
	var status windows.IO_STATUS_BLOCK
	err := windows.NtQueryInformationFile(handle, &status, (*byte)(unsafe.Pointer(&mode)), uint32(unsafe.Sizeof(mode)), fileModeInformation)
	if err != nil {
		return "error(" + err.Error() + ")"
	}
	if mode&(windows.FILE_SYNCHRONOUS_IO_ALERT|windows.FILE_SYNCHRONOUS_IO_NONALERT) != 0 {
		return "sync"
	}
	return "async"
}

// TestRunGivesChildAsynchronousOutputHandles proves the standard output and standard error handles
// a child inherits from Run complete writes asynchronously, through the test executable's child
// fixture reading its own handles' FILE_MODE_INFORMATION.
func TestRunGivesChildAsynchronousOutputHandles(t *testing.T) {
	var stdout bytes.Buffer
	if err := capture.Run(helper(context.Background(), t, "handle-modes"), &stdout, nil); err != nil {
		t.Fatalf("Run: %v (%s)", err, stdout.String())
	}
	if stdout.String() != "stdout=async stderr=async" {
		t.Fatalf("child handle modes %q; want asynchronous output handles", stdout.String())
	}
}
