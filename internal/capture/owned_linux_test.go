package capture_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// assertOwnedProcessExited distinguishes a dead or unreaped zombie process from running work.
func assertOwnedProcessExited(t *testing.T, pid int) {
	t.Helper()
	if pid <= 0 {
		t.Fatal("missing descendant PID")
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	_, tail, ok := strings.Cut(string(data), ") ")
	if !ok || !strings.HasPrefix(tail, "Z ") {
		t.Fatalf("owned descendant still exists: %s", data)
	}
}
