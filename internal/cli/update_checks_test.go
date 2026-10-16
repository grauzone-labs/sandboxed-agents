package cli_test

import (
	"strings"
	"testing"
)

// TestUpdateUsageBeforeFailingPreflight proves invalid update combinations issue no Podman call even on an unsuitable host through the executable CLI seam.
func TestUpdateUsageBeforeFailingPreflight(t *testing.T) {
	for _, args := range [][]string{
		{"update"}, {"update", "demo", "--all"}, {"update", "--all", "demo"},
		{"update", "--all", "--with", "native"}, {"update", "demo", "--with", "unknown"},
		{"update", "demo", "--with", "none,native"}, {"update", "BAD/NAME"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f := linuxHost(t)
			f.responses["--version"] = response{Exit: 125, Output: "preflight injection"}
			status, output, calls := runUpExecutable(t, f, args...)
			if status != 2 || !strings.Contains(output, "usage:") || len(calls) != 0 {
				t.Fatalf("status=%d output=%s calls=%v", status, output, calls)
			}
		})
	}
}

// TestUpdatePreflightBeforeUnknownSandbox proves a failing preflight precedes lookup of an unknown name and all image work through the executable CLI seam.
func TestUpdatePreflightBeforeUnknownSandbox(t *testing.T) {
	f := linuxHost(t)
	f.responses["--version"] = response{Exit: 125, Output: "preflight injection"}
	status, output, calls := runUpExecutable(t, f, "update", "unknown")
	if status != 1 || !strings.Contains(output, "step 2:") || strings.Contains(output, "does not exist") {
		t.Fatalf("status=%d output=%s", status, output)
	}
	for _, call := range calls {
		switch call[0] {
		case "container", "volume", "image", "build", "rename", "create":
			t.Fatalf("sandbox or image call before passing preflight: %v", call)
		}
	}
}
