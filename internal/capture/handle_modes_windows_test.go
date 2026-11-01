//go:build windows

package capture_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"
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

// inputHandleMode reports the inherited standard input handle's asynchronous completion mode.
func inputHandleMode() string { return handleMode(windows.Handle(os.Stdin.Fd())) }

// TestInputPipeGivesChildAsynchronousInput proves the child input handle supports inbox OpenSSH asynchronous reads through capture's process seam.
func TestInputPipeGivesChildAsynchronousInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := helper(ctx, t, "input-handle-mode")
	reader, writer, err := capture.InputPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	command.Stdin = reader
	var out bytes.Buffer
	if err := capture.Run(command, &out, nil); err != nil {
		t.Fatal(err)
	}
	if out.String() != "async" {
		t.Fatalf("stdin mode=%q", &out)
	}
}
