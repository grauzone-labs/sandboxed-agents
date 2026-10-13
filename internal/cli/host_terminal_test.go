package cli

import (
	"os"
	"testing"
)

// TestHostTerminalRejectsNullDevice proves both default terminal predicates reject the host's null device through Host terminal detection.
func TestHostTerminalRejectsNullDevice(t *testing.T) {
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	host := (Host{}).defaults()
	if host.IsInputTerminal(input) {
		t.Fatalf("null device %s detected as a terminal", os.DevNull)
	}
	if host.IsOutputTerminal(input) {
		t.Fatalf("null device %s detected as an output terminal", os.DevNull)
	}
}
