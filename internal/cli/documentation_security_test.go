package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSecurityPolicyCoversPodmanCreateOptions proves SECURITY.md names every Podman option actually issued by up at the CLI boundary with fake Podman, and omission fails the check.
func TestSecurityPolicyCoversPodmanCreateOptions(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "SECURITY.md"))
	if err != nil {
		t.Fatal(err)
	}
	f := linuxHost(t)
	scriptNewSandbox(t, f, "default", "agent01")
	status, output, calls := f.run(t, "up", "agent01")
	if status != 0 {
		t.Fatalf("up status=%d output=%s", status, output)
	}
	var create []string
	for _, call := range calls {
		if len(call) > 0 && call[0] == "create" {
			create = call
		}
	}
	if len(create) == 0 {
		t.Fatal("up issued no Podman create call")
	}
	if err := securityOptionsError(string(data), create); err != nil {
		t.Fatal(err)
	}
	for _, option := range create {
		if !strings.HasPrefix(option, "--") {
			continue
		}
		// Remove every occurrence so prose, tables, or repeated examples cannot
		// hide a missing policy option from the check.
		without := strings.ReplaceAll(string(data), option, "OMITTED")
		if err := securityOptionsError(without, create); err == nil {
			t.Errorf("security check accepted omitted %s", option)
		}
	}
}

// securityOptionsError checks that SECURITY.md documents every Podman create option supplied by
// the CLI.
func securityOptionsError(document string, create []string) error {
	for _, option := range create {
		if strings.HasPrefix(option, "--") && !strings.Contains(document, "`"+option+"`") {
			return fmt.Errorf("SECURITY.md is missing Podman create option `%s`", option)
		}
	}
	return nil
}
