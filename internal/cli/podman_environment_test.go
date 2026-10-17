package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// logAmbientPodmanEnvironment sets ambient Podman target variables and enables environment
// capture by the fake Podman process.
func logAmbientPodmanEnvironment(t *testing.T) string {
	t.Helper()
	for _, key := range []string{"CONTAINER_HOST", "CONTAINER_CONNECTION", "CONTAINER_SSHKEY"} {
		t.Setenv(key, "ambient-target")
	}
	envLog := filepath.Join(t.TempDir(), "environment.json")
	t.Setenv("TEST_PODMAN_ENV_LOG", envLog)
	return envLog
}

// assertNoAmbientPodmanEnvironment checks the captured child-process environment for leaked
// ambient Podman target variables.
func assertNoAmbientPodmanEnvironment(t *testing.T, envLog string) {
	t.Helper()
	data, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatal(err)
	}
	var environment []string
	if err := json.Unmarshal(data, &environment); err != nil {
		t.Fatal(err)
	}
	for _, entry := range environment {
		if strings.HasPrefix(entry, "CONTAINER_HOST=") || strings.HasPrefix(entry, "CONTAINER_CONNECTION=") || strings.HasPrefix(entry, "CONTAINER_SSHKEY=") {
			t.Fatalf("ambient remote setting survived: %s", entry)
		}
	}
}
