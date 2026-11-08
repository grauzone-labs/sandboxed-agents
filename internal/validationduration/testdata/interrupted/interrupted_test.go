package interrupted_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/release"
)

// TestLiveSuite supplies real verbose subtest output and a failing schema-2
// record for the offline collector test; it never invokes a live host command.
func TestLiveSuite(t *testing.T) {
	directory := os.Getenv("TEST_DURATION_DIRECTORY")
	if directory == "" {
		t.Skip("offline subprocess fixture only")
	}
	_, err := release.WriteLiveSuiteRecord(directory, release.LiveSuiteRecord{
		Commit: "0123456789abcdef0123456789abcdef01234567", Platform: "linux", Result: "fail",
		Checks: []release.LiveSuiteCheck{{Name: "preflight", Result: "not-run"}, {Name: "lifecycle", Result: "not-run"}, {Name: "agent", Result: "not-run"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("preflight", func(t *testing.T) { t.Log("offline preflight finished") })
	t.Run("lifecycle", func(t *testing.T) { t.Log("offline lifecycle finished") })
	t.Run("agent", func(t *testing.T) {
		fmt.Println("OFFLINE_LATER_CASE_MARKER")
		if err := os.WriteFile(filepath.Join(directory, "ready"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
		t.Fatal("fixture was not killed")
	})
}
