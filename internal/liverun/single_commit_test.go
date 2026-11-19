package liverun

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// TestSingleCommitReceiptPreservesFailedBuild proves the producer/artifact seam retains a confirmed v2 result even when the original build returns an error.
func TestSingleCommitReceiptPreservesFailedBuild(t *testing.T) {
	o, before, directory := buildChannelFixture(t)
	asset := strings.Repeat("e", 64)
	id := "sha256:" + strings.Repeat("b", 64)
	failure := errors.New("actual build failed after commit")
	err := CaptureSingleCommitBuild(directory, "localhost/sandboxed-agents:none-"+asset, asset, false, func(path string) error {
		if err := os.WriteFile(path, []byte(id), 0600); err != nil {
			t.Fatal(err)
		}
		return failure
	}, func(string) (string, error) { return id, nil })
	results, readErr := readBuildResults(o, before)
	if !errors.Is(err, failure) || readErr != nil || len(results) != 1 || results[0].ID != id || results[0].Policy != "single-commit" || results[0].Outcome != "committed-build-error" {
		t.Fatalf("confirmed failed final lost: err=%v read=%v results=%v", err, readErr, results)
	}
}

// TestSingleCommitUnconfirmedResultsStayOpen proves missing, mismatched and unpublished results cannot close a typed attempt at the producer/artifact seam or change the build outcome.
func TestSingleCommitUnconfirmedResultsStayOpen(t *testing.T) {
	for _, kind := range []string{"missing-iid", "mismatch", "inspect-error", "publication-error"} {
		t.Run(kind, func(t *testing.T) {
			o, before, directory := buildChannelFixture(t)
			id := "sha256:" + strings.Repeat("b", 64)
			asset := strings.Repeat("e", 64)
			err := CaptureSingleCommitBuild(directory, "localhost/sandboxed-agents:none-"+asset, asset, false, func(path string) error {
				if kind == "missing-iid" {
					return nil
				}
				return os.WriteFile(path, []byte(id), 0600)
			}, func(string) (string, error) {
				switch kind {
				case "mismatch":
					return "sha256:" + strings.Repeat("c", 64), nil
				case "inspect-error":
					return "", errors.New("private diagnostic")
				case "publication-error":
					if err := os.Rename(directory, directory+"-moved"); err != nil {
						t.Fatal(err)
					}
				}
				return id, nil
			})
			if kind == "publication-error" {
				if err := os.Rename(directory+"-moved", directory); err != nil {
					t.Fatal(err)
				}
			}
			state := readBuildChannel(o, before)
			if err != nil || !state.Open || len(state.Results) != 0 {
				t.Fatalf("unconfirmed result admitted or outcome changed: %v %+v", err, state)
			}
		})
	}
}
