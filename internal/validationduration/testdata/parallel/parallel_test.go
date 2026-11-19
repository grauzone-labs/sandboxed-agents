package parallel_test

import "testing"

// TestLiveSuite emits actual verbose output with concurrent fixed cases, one
// failed check, skipped images, and an unknown nested check for the collector.
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
