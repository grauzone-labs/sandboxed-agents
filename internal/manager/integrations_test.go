package manager_test

import (
	"bytes"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// TestGitCredentialsConfiguresHomeStore proves the credential workflow requests exactly the specified global Git helper as agent without other processes at the manager command seam.
func TestGitCredentialsConfiguresHomeStore(t *testing.T) {
	effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	var started []manager.Process
	effects.Start = func(process manager.Process) error { started = append(started, process); return nil }
	var stdout, stderr bytes.Buffer
	status := manager.RunWithEffects([]string{"integrations", "config", "git", "credentials"}, &stdout, &stderr, effects)
	if status != 0 || len(started) != 1 {
		t.Fatalf("status=%d processes=%v stderr=%s", status, started, &stderr)
	}
	got := started[0]
	want := []string{"config", "--global", "--replace-all", "--", "credential.helper", "store --file=/home/agent/.git-credentials"}
	if got.Program != "git" || got.Identity != (manager.Identity{UID: 1000, GID: 1000}) || !reflect.DeepEqual(got.Args, want) {
		t.Fatalf("process=%+v want git %q as agent", got, want)
	}
}

// TestGitIdentityWritesBothGlobalValues proves complete identity requests write literal name and email as agent and incomplete requests change nothing at the manager process seam.
func TestGitIdentityWritesBothGlobalValues(t *testing.T) {
	for _, tc := range []struct {
		options []string
		status  int
		calls   [][]string
	}{
		{[]string{"--name", "A Person", "--email", "a@example.com"}, 0, [][]string{{"config", "--global", "--replace-all", "--", "user.name", "A Person"}, {"config", "--global", "--replace-all", "--", "user.email", "a@example.com"}}},
		{[]string{"--name", "N"}, 1, nil},
		{[]string{"--email", "E"}, 1, nil},
		{nil, 1, nil},
	} {
		t.Run(strings.Join(tc.options, " "), func(t *testing.T) {
			effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
			var calls [][]string
			effects.Start = func(process manager.Process) error {
				if process.Program != "git" || process.Identity != (manager.Identity{UID: 1000, GID: 1000}) {
					t.Errorf("process=%+v", process)
				}
				calls = append(calls, process.Args)
				return nil
			}
			var out bytes.Buffer
			args := append([]string{"integrations", "config", "git", "identity"}, tc.options...)
			if status := manager.RunWithEffects(args, &out, &out, effects); status != tc.status || !reflect.DeepEqual(calls, tc.calls) {
				t.Fatalf("status=%d calls=%v output=%s want status=%d calls=%v", status, calls, &out, tc.status, tc.calls)
			}
		})
	}
}

// TestGitWorkflowFailuresStopWithoutFallback proves Git failures return a command failure and never attempt later writes or alternate helpers at the manager process seam.
func TestGitWorkflowFailuresStopWithoutFallback(t *testing.T) {
	for _, args := range [][]string{{"integrations", "config", "git", "credentials"}, {"integrations", "config", "git", "identity", "--name", "N", "--email", "E"}} {
		for _, failureAt := range []int{1, 2} {
			if args[3] == "credentials" && failureAt == 2 {
				continue
			}
			effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
			starts := 0
			effects.Start = func(manager.Process) error {
				starts++
				if starts == failureAt {
					return childExit(23)
				}
				return nil
			}
			var out bytes.Buffer
			if status := manager.RunWithEffects(args, &out, &out, effects); status != 1 || starts != failureAt {
				t.Fatalf("status=%d starts=%d output=%s", status, starts, &out)
			}
		}
	}
}

// TestGitWorkflowsWriteGlobalConfigOnly proves real Git replaces helper entries and writes identity only in an isolated home through the manager command and injected process seam.
func TestGitWorkflowsWriteGlobalConfigOnly(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("real Git is unavailable for isolated-home verification")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, ".gitconfig")
	if err := os.WriteFile(config, []byte("[credential]\n\thelper = old-one\n\thelper = old-two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	environment := []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "GIT_CONFIG_GLOBAL=" + config, "GIT_CONFIG_NOSYSTEM=1"}
	effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	effects.Start = func(process manager.Process) error {
		if process.Program != "git" || process.Identity != (manager.Identity{UID: 1000, GID: 1000}) {
			t.Fatalf("unexpected process=%+v", process)
		}
		command := exec.Command(git, process.Args...)
		command.Env = environment
		command.Dir = home
		command.Stdout = process.Stdout
		command.Stderr = process.Stderr
		return command.Run()
	}
	var output bytes.Buffer
	for _, args := range [][]string{{"integrations", "config", "git", "credentials"}, {"integrations", "config", "git", "identity", "--name", "A Person", "--email", "a@example.com"}} {
		if status := manager.RunWithEffects(args, &output, &output, effects); status != 0 {
			t.Fatalf("status=%d output=%s", status, &output)
		}
	}
	for key, want := range map[string]string{"credential.helper": "store --file=/home/agent/.git-credentials\n", "user.name": "A Person\n", "user.email": "a@example.com\n"} {
		command := exec.Command(git, "config", "--global", "--get-all", key)
		command.Env = environment
		command.Dir = home
		got, err := command.CombinedOutput()
		if err != nil || string(got) != want {
			t.Fatalf("key=%s output=%q error=%v want=%q", key, got, err, want)
		}
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 1 || entries[0].Name() != ".gitconfig" {
		t.Fatalf("home entries=%v error=%v; want only global config, no credential file", entries, err)
	}
	entries, err = os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "home" {
		t.Fatalf("outside-home entries=%v error=%v", entries, err)
	}
}

// TestGitConfigRejectsUndeliveredWorkflow proves the manager refuses workflows absent from the shared integration catalog before Git at its public command seam.
func TestGitConfigRejectsUndeliveredWorkflow(t *testing.T) {
	original := catalog.Default.Integrations["git"]
	integration := original
	integration.ConfigWorkflows = maps.Clone(original.ConfigWorkflows)
	delete(integration.ConfigWorkflows, "credentials")
	catalog.Default.Integrations["git"] = integration
	t.Cleanup(func() { catalog.Default.Integrations["git"] = original })
	effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	var output bytes.Buffer
	status := manager.RunWithEffects([]string{"integrations", "config", "git", "credentials"}, &output, &output, effects)
	if status != 2 || !strings.Contains(output.String(), "valid names: identity") {
		t.Fatalf("status=%d output=%s; want catalog validation before Git", status, &output)
	}
}

// TestManagerUsageListsDeliveredConfigWorkflows proves manager usage follows shared workflow names and options at its public command seam.
func TestManagerUsageListsDeliveredConfigWorkflows(t *testing.T) {
	original := catalog.Default.Integrations["git"]
	integration := original
	integration.ConfigWorkflows = maps.Clone(original.ConfigWorkflows)
	delete(integration.ConfigWorkflows, "credentials")
	catalog.Default.Integrations["git"] = integration
	t.Cleanup(func() { catalog.Default.Integrations["git"] = original })
	var output bytes.Buffer
	status := manager.RunWithEffects([]string{"unknown"}, &output, &output, noProcesses(t, manager.Identity{UID: 1000, GID: 1000}))
	if status != 2 || !strings.Contains(output.String(), "integrations config git identity --name NAME --email EMAIL") || strings.Contains(output.String(), "credentials") {
		t.Fatalf("status=%d output=%s; want only delivered workflow usage", status, &output)
	}
}
