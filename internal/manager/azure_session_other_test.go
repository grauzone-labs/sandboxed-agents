//go:build !linux

package manager_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// TestAzureSessionReportsUnsupportedRuntime proves non-Linux private Azure commands verify identity and explain their refusal without starting a process through the manager command seam.
func TestAzureSessionReportsUnsupportedRuntime(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		diagnostic string
	}{
		{[]string{"__azure-login", strings.Repeat("a", 64), "agent01"}, "Azure browser sessions require the Linux manager runtime\n"},
		{[]string{"__azure-control", strings.Repeat("a", 64), "cancel"}, "Azure browser session control requires the Linux manager runtime\n"},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			for _, identity := range []struct {
				name       string
				id         int
				diagnostic string
			}{
				{"agent", 1000, tc.diagnostic},
				{"root", 0, "agent work requires real, effective, and saved UID and GID 1000\n"},
			} {
				t.Run(identity.name, func(t *testing.T) {
					effects := manager.Effects{
						UIDs:   func() (int, int, int) { return identity.id, identity.id, identity.id },
						GIDs:   func() (int, int, int) { return identity.id, identity.id, identity.id },
						Groups: func() ([]int, error) { return nil, nil },
						Spawn: func(manager.Process) (manager.Child, error) {
							t.Fatal("unexpected process start")
							return nil, nil
						},
					}
					var stdout, stderr bytes.Buffer
					status := manager.RunWithEffects(tc.args, &stdout, &stderr, effects)
					if status != 1 || stdout.Len() != 0 || stderr.String() != identity.diagnostic {
						t.Fatalf("status=%d stdout=%q stderr=%q; want refusal %q", status, &stdout, &stderr, identity.diagnostic)
					}
				})
			}
		})
	}
}
