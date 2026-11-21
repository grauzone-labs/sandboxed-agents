package liverun

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseOptionsResolvesDirectoryAliases proves the prepare option seam refuses a record directory that a symbolic
// link makes the same physical directory as the run directory, whether or not the run directory exists yet, and still
// accepts distinct directories.
func TestParseOptionsResolvesDirectoryAliases(t *testing.T) {
	for _, tc := range []struct {
		name     string
		alias    bool
		existing bool
	}{
		{"existing-alias", true, true},
		{"future-alias", true, false},
		{"distinct", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			run, record := filepath.Join(root, "run"), filepath.Join(root, "record")
			if tc.existing {
				if err := os.Mkdir(run, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.alias {
				if err := os.Symlink(run, record); err != nil {
					t.Skipf("symbolic links unavailable: %v", err)
				}
			}
			svc := &Service{CoordinationRoot: filepath.Join(root, "locks")}
			_, err := svc.parseOptions([]string{"-checkout", filepath.Join(root, "checkout"), "-run-dir", run, "-record-dir", record, "-revision", strings.Repeat("a", 40)}, io.Discard)
			if tc.alias != (err != nil) {
				t.Fatalf("alias=%v, got %v", tc.alias, err)
			}
		})
	}
}

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
