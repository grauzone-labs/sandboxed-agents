package parallel_test

import "testing"

// TestLiveSuite proves concurrent pass/fail/skip cases and nested output remain
// distinguishable at the verbose Go subprocess seam used by the collector.
func TestLiveSuite(t *testing.T) {
	t.Run("preflight", func(t *testing.T) { t.Log("private preflight output") })
	for _, name := range []string{"lifecycle", "update", "agent", "t3code", "images"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			t.Log("private parallel output")
			switch name {
			case "update":
				t.Error("scripted failure")
			case "images":
				t.Skip("image coverage not selected")
			default:
				t.Run("nested", func(t *testing.T) { t.Log("private nested output") })
			}
		})
	}
}
