package liverun

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// TestFailCheckReportsUnsavedDiagnostic proves the failed-check report seam says when the private diagnostic could not
// be written, for plain and engine checks, without exposing the raw engine output.
func TestFailCheckReportsUnsavedDiagnostic(t *testing.T) {
	o := Options{RunDir: filepath.Join(t.TempDir(), "missing")}
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"inventory", fmt.Errorf("raw engine output /secret/path")},
		{"engine", engineFault("engine unreachable", "connection refused; check the selected Podman service", fmt.Errorf("raw engine output /secret/path"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := failCheck(o, tc.name, tc.err).Error()
			if !strings.Contains(text, "private diagnostic could not be retained") || strings.Contains(text, "diagnostic retained") || strings.Contains(text, "/secret/path") {
				t.Fatalf("unsaved diagnostic misreported: %s", text)
			}
		})
	}
}
