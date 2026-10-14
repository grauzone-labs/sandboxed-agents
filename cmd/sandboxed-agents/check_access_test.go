package main_test

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

func checkAccessForms() [][]string {
	return [][]string{
		{"shell", "demo"},
		{"fingerprint", "demo"},
		{"ssh-config", "demo"},
		{"ssh-config", "demo", "--install"},
		{"ssh-config", "demo", "--remove"},
	}
}

func checkAccessInstalledSetup(t *testing.T) testutil.Fakes {
	t.Helper()
	fakes := sshSetupHarness(t, installResponses())
	dir := filepath.Join(os.Getenv("HOME"), ".ssh")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"config":      "# personal configuration\nHost personal\n HostName example.com\n",
		"id_personal": "personal private key bytes",
		"known_hosts": "personal host key bytes\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--install"); status != 0 {
		t.Fatalf("install fixture=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
	for _, name := range []string{"id_ed25519", "id_ed25519.pub", "entry", "known_hosts"} {
		found := false
		for path := range setupFiles(t) {
			if filepath.Base(path) == name && strings.Contains(path, "sandbox-") {
				found = true
			}
		}
		if !found {
			t.Fatalf("installed fixture lacks managed %s", name)
		}
	}
	return fakes
}

type checkAccessEvidence struct {
	files                       map[string]string
	podmanCalls, sshCalls, keys int
}

func checkAccessBefore(t *testing.T, fakes testutil.Fakes) checkAccessEvidence {
	t.Helper()
	return checkAccessEvidence{
		files:       setupFiles(t),
		podmanCalls: len(testutil.Invocations(t, fakes.PodmanLog)),
		sshCalls:    len(testutil.Invocations(t, fakes.SSHLog)),
		keys:        len(testutil.Invocations(t, os.Getenv("TEST_SSH_KEYGEN_LOG"))),
	}
}

func checkAccessQueriesOnly(t *testing.T, fakes testutil.Fakes, before checkAccessEvidence) {
	t.Helper()
	for _, invocation := range testutil.Invocations(t, fakes.PodmanLog)[before.podmanCalls:] {
		call := invocation
		if len(call) >= 2 && call[0] == "--connection" {
			call = call[2:]
		}
		if len(call) >= 2 {
			if (call[0] == "container" || call[0] == "volume") && (call[1] == "exists" || call[1] == "inspect") {
				continue
			}
			if call[0] == "machine" && (call[1] == "list" || call[1] == "inspect") {
				continue
			}
		}
		t.Errorf("access safeguard issued more than an object or target query: %v", invocation)
	}
	if after := len(testutil.Invocations(t, fakes.SSHLog)); after != before.sshCalls {
		t.Errorf("access safeguard ran SSH: calls before=%d after=%d", before.sshCalls, after)
	}
	if after := len(testutil.Invocations(t, os.Getenv("TEST_SSH_KEYGEN_LOG"))); after != before.keys {
		t.Errorf("access safeguard generated a key: calls before=%d after=%d", before.keys, after)
	}
}

func checkAccessUnchanged(t *testing.T, fakes testutil.Fakes, before checkAccessEvidence) {
	t.Helper()
	checkAccessQueriesOnly(t, fakes, before)
	// setupFiles ignores the persistent empty lifecycle locks, as in the existing SSH setup tests.
	if after := setupFiles(t); !reflect.DeepEqual(after, before.files) {
		t.Errorf("access refusal changed SSH files: before=%v after=%v", before.files, after)
	}
}

// TestCheckAccessOwnerConflictsPrecedeStoppedAndBackup proves missing or different owners on the container, every volume, and a backup refuse all access forms before stopped or interrupted-update errors without changing files through the executable CLI and fake Podman/SSH seam.
func TestCheckAccessOwnerConflictsPrecedeStoppedAndBackup(t *testing.T) {
	for _, object := range []string{"container", "workspace", "home", "ssh", "backup"} {
		for _, owner := range []string{"", "foreign"} {
			for _, backup := range []bool{false, true} {
				if object == "backup" && !backup {
					continue
				}
				state := "stopped"
				if backup {
					state += "-with-backup"
				}
				t.Run(object+"/owner="+owner+"/"+state, func(t *testing.T) {
					fakes := checkAccessInstalledSetup(t)
					responses := setupResponses(false)
					for _, kind := range []string{"workspace", "home", "ssh"} {
						name := "sandboxed-agents.default.demo." + kind
						responses["volume exists "+name] = lifecycleResponse{}
						responses["volume inspect "+name] = lifecycleResponse{Output: `[{"Labels":{"io.sandboxed-agents.owner":"default"}}]`}
					}
					if backup {
						responses["container exists sandboxed-agents-backup.default.demo"] = lifecycleResponse{}
						responses["container inspect sandboxed-agents-backup.default.demo"] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}}}]`}
					}
					name := "sandboxed-agents.default.demo"
					labels := `{}`
					if owner != "" {
						labels = `{"io.sandboxed-agents.owner":"` + owner + `"}`
					}
					switch object {
					case "container":
						responses["container inspect "+name] = lifecycleResponse{Output: `[{"Config":{"Labels":` + labels + `},"State":{"Running":false}}]`}
					case "backup":
						name = "sandboxed-agents-backup.default.demo"
						responses["container inspect "+name] = lifecycleResponse{Output: `[{"Config":{"Labels":` + labels + `}}]`}
					default:
						name += "." + object
						responses["volume inspect "+name] = lifecycleResponse{Output: `[{"Labels":` + labels + `}]`}
					}
					scriptSetupResponses(t, responses, runtime.GOOS)
					for _, args := range checkAccessForms() {
						t.Run(strings.Join(args, " "), func(t *testing.T) {
							before := checkAccessBefore(t, fakes)
							status, stdout, stderr := sshSetupRun(t, args...)
							if status != 1 || stdout != "" || !strings.Contains(stderr, "owner conflict") || !strings.Contains(stderr, name) || !strings.Contains(stderr, "Podman") || strings.Contains(stderr, "start demo") || strings.Contains(stderr, "update demo") {
								t.Errorf("owner refusal=%d stdout=%q stderr=%q; want conflict naming %s and Podman before later errors", status, stdout, stderr, name)
							}
							checkAccessUnchanged(t, fakes, before)
						})
					}
				})
			}
		}
	}
}

// TestCheckAccessInterruptedUpdateRefusesStoppedAndRunning proves all access forms name update for an owned backup before stopped or action checks and preserve an installed SSH setup through the executable CLI and fake Podman/SSH seam.
func TestCheckAccessInterruptedUpdateRefusesStoppedAndRunning(t *testing.T) {
	for _, running := range []bool{false, true} {
		state := "stopped"
		if running {
			state = "running"
		}
		t.Run(state, func(t *testing.T) {
			fakes := checkAccessInstalledSetup(t)
			responses := setupResponses(running)
			responses["container exists sandboxed-agents-backup.default.demo"] = lifecycleResponse{}
			responses["container inspect sandboxed-agents-backup.default.demo"] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}}}]`}
			scriptSetupResponses(t, responses, runtime.GOOS)
			for _, args := range checkAccessForms() {
				t.Run(strings.Join(args, " "), func(t *testing.T) {
					before := checkAccessBefore(t, fakes)
					status, stdout, stderr := sshSetupRun(t, args...)
					if status != 1 || stdout != "" || !strings.Contains(stderr, "interrupted update") || !strings.Contains(stderr, "update demo") || strings.Contains(stderr, "start demo") {
						t.Errorf("interrupted refusal=%d stdout=%q stderr=%q; want update demo before later errors", status, stdout, stderr)
					}
					checkAccessUnchanged(t, fakes, before)
				})
			}
		})
	}
}

func checkAccessVolumeSets() [][]string {
	return [][]string{
		{"workspace"}, {"home"}, {"ssh"},
		{"workspace", "home"}, {"workspace", "ssh"}, {"home", "ssh"},
		{"workspace", "home", "ssh"},
	}
}

func checkAccessVolumesOnlyResponses(kinds []string) map[string]lifecycleResponse {
	responses := setupResponses(false)
	responses["container exists sandboxed-agents.default.demo"] = lifecycleResponse{Exit: 1}
	for _, kind := range kinds {
		name := "sandboxed-agents.default.demo." + kind
		responses["volume exists "+name] = lifecycleResponse{}
		responses["volume inspect "+name] = lifecycleResponse{Output: `[{"Labels":{"io.sandboxed-agents.owner":"default"}}]`}
	}
	return responses
}

// TestCheckAccessVolumesOnlyRefusesContainerAccess proves every partial or complete set of kept volumes refuses shell, fingerprint, SSH preview/install, and start --ssh-config with up guidance and no file changes through the executable CLI and fake Podman/SSH seam.
func TestCheckAccessVolumesOnlyRefusesContainerAccess(t *testing.T) {
	for _, kinds := range checkAccessVolumeSets() {
		t.Run(strings.Join(kinds, "+"), func(t *testing.T) {
			fakes := checkAccessInstalledSetup(t)
			scriptSetupResponses(t, checkAccessVolumesOnlyResponses(kinds), runtime.GOOS)
			forms := [][]string{
				{"shell", "demo"}, {"fingerprint", "demo"},
				{"ssh-config", "demo"}, {"ssh-config", "demo", "--install"},
				{"start", "demo", "--ssh-config"},
			}
			for _, args := range forms {
				t.Run(strings.Join(args, " "), func(t *testing.T) {
					before := checkAccessBefore(t, fakes)
					status, stdout, stderr := sshSetupRun(t, args...)
					if status != 1 || stdout != "" || !strings.Contains(stderr, "only volumes") || !strings.Contains(stderr, "up demo") || strings.Contains(stderr, "does not exist") || strings.Contains(stderr, "start demo") {
						t.Errorf("volumes-only refusal=%d stdout=%q stderr=%q; want known sandbox and up demo", status, stdout, stderr)
					}
					checkAccessUnchanged(t, fakes, before)
				})
			}
		})
	}
}

// TestCheckAccessVolumesOnlySSHRemovalCleansHost proves ssh-config --remove deletes the managed key pair, entry, pin, and Include for every partial or complete set of kept volumes, preserving personal SSH files without touching Podman objects through the executable CLI and fake Podman/SSH seam.
func TestCheckAccessVolumesOnlySSHRemovalCleansHost(t *testing.T) {
	for _, kinds := range checkAccessVolumeSets() {
		t.Run(strings.Join(kinds, "+"), func(t *testing.T) {
			fakes := checkAccessInstalledSetup(t)
			scriptSetupResponses(t, checkAccessVolumesOnlyResponses(kinds), runtime.GOOS)
			before := checkAccessBefore(t, fakes)
			status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--remove")
			if status != 0 || stderr != "" || !strings.Contains(stdout, "removed SSH setup for demo") || !strings.Contains(stdout, "authorization remains") || !strings.Contains(stdout, "ssh-config demo --install") {
				t.Errorf("volumes-only cleanup=%d stdout=%q stderr=%q", status, stdout, stderr)
			}
			checkAccessQueriesOnly(t, fakes, before)
			dir := filepath.Join(os.Getenv("HOME"), ".ssh")
			want := map[string]string{
				filepath.Join(dir, "config"):      "# personal configuration\nHost personal\n HostName example.com\n",
				filepath.Join(dir, "id_personal"): "personal private key bytes",
				filepath.Join(dir, "known_hosts"): "personal host key bytes\n",
			}
			if after := setupFiles(t); !reflect.DeepEqual(after, want) {
				t.Errorf("cleanup did not leave exactly the original personal files: after=%v want=%v", after, want)
			}
		})
	}
}
