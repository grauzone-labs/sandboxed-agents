package manager_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// TestAzureDevOpsLoginStoresCredentials proves the manager sends a PAT only on stdin to Azure CLI and Git as agent through injected process functions.
func TestAzureDevOpsLoginStoresCredentials(t *testing.T) {
	effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	effects.Stdin = strings.NewReader("pat-sentinel\n")
	var calls [][]string
	var inputs []string
	effects.Start = func(p manager.Process) error {
		if p.Identity != (manager.Identity{UID: 1000, GID: 1000}) {
			t.Errorf("identity=%v", p.Identity)
		}
		calls = append(calls, append([]string{p.Program}, p.Args...))
		input := ""
		if p.Stdin != nil {
			data, err := io.ReadAll(p.Stdin)
			if err != nil {
				t.Fatal(err)
			}
			input = string(data)
		}
		inputs = append(inputs, input)
		return nil
	}
	var out bytes.Buffer
	status := manager.RunWithEffects([]string{"integrations", "login", "azdo", "--organization", "https://dev.azure.com/contoso"}, &out, &out, effects)
	want := [][]string{
		{"az", "devops", "login", "--organization", "https://dev.azure.com/contoso", "--only-show-errors"},
		{"git", "config", "--global", "--replace-all", "--", "credential.helper", "store --file=/home/agent/.git-credentials"},
		{"git", "config", "--global", "--replace-all", "--", "credential.https://dev.azure.com/contoso.username", "contoso"},
		{"git", "-c", "credential.helper=", "-c", "credential.helper=store --file=/home/agent/.git-credentials", "-c", "credential.useHttpPath=false", "credential", "approve"},
	}
	if status != 0 || !reflect.DeepEqual(calls, want) {
		t.Fatalf("status=%d calls=%q output=%s", status, calls, &out)
	}
	if !reflect.DeepEqual(inputs, []string{"pat-sentinel\n", "", "", "url=https://dev.azure.com/contoso\nusername=contoso\npassword=pat-sentinel\n\n"}) {
		t.Fatalf("credential stdin was not delivered as required")
	}
	if strings.Contains(out.String(), "pat-sentinel") {
		t.Fatal("token leaked")
	}
}

// TestAzureDevOpsCredentialsResolveForClone proves the login's stored credential is usable for plain and username-qualified Azure clone URLs through the manager and real Git credential seam.
func TestAzureDevOpsCredentialsResolveForClone(t *testing.T) {
	home := t.TempDir()
	repository := t.TempDir()
	if data, err := exec.Command("git", "init", repository).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, data)
	}
	local := exec.Command("git", "-C", repository, "config", "credential.helper", "!touch "+filepath.Join(repository, "helper-ran")+"; exit 1")
	if err := local.Run(); err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(), "HOME="+home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(home, ".gitconfig"), "GIT_TERMINAL_PROMPT=0")
	effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	effects.Stdin = strings.NewReader("git-pat-sentinel\n")
	effects.Start = func(p manager.Process) error {
		if p.Program == "az" {
			return nil
		}
		args := append([]string(nil), p.Args...)
		for i, arg := range args {
			args[i] = strings.ReplaceAll(arg, "/home/agent/.git-credentials", filepath.Join(home, ".git-credentials"))
		}
		cmd := exec.Command("git", args...)
		cmd.Env = environment
		cmd.Dir = repository
		cmd.Stdin = p.Stdin
		cmd.Stdout = p.Stdout
		cmd.Stderr = p.Stderr
		return cmd.Run()
	}
	var out bytes.Buffer
	if status := manager.RunWithEffects([]string{"integrations", "login", "azdo", "--organization", "https://dev.azure.com/contoso/"}, &out, &out, effects); status != 0 {
		t.Fatalf("status=%d output=%s", status, &out)
	}
	for _, url := range []string{"https://dev.azure.com/contoso/project/_git/repository", "https://contoso@dev.azure.com/contoso/project/_git/repository"} {
		cmd := exec.Command("git", "credential", "fill")
		cmd.Env = environment
		cmd.Stdin = strings.NewReader("url=" + url + "\n\n")
		data, err := cmd.Output()
		if err != nil || !strings.Contains(string(data), "password=git-pat-sentinel\n") {
			t.Fatalf("stored credential did not resolve for clone URL: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(repository, "helper-ran")); !os.IsNotExist(err) {
		t.Fatal("login exposed credential to a repository helper")
	}
	info, err := os.Stat(filepath.Join(home, ".git-credentials"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("credential file mode=%v", info.Mode().Perm())
	}
}

// TestAzureDevOpsLoginFailuresDoNotDiscloseToken proves every child failure is normalized and stops later credential changes without exposing child output or errors through the manager seam.
func TestAzureDevOpsLoginFailuresDoNotDiscloseToken(t *testing.T) {
	for failureAt := 1; failureAt <= 4; failureAt++ {
		effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
		effects.Stdin = strings.NewReader("failure-pat-sentinel\n")
		starts := 0
		effects.Start = func(p manager.Process) error {
			starts++
			fmt.Fprintln(p.Stdout, "failure-pat-sentinel")
			fmt.Fprintln(p.Stderr, "failure-pat-sentinel")
			if starts == failureAt {
				return errors.New("failure-pat-sentinel")
			}
			return nil
		}
		var out bytes.Buffer
		status := manager.RunWithEffects([]string{"integrations", "login", "azdo", "--organization", "https://dev.azure.com/contoso"}, &out, &out, effects)
		if status != 1 || starts != failureAt || strings.Contains(out.String(), "failure-pat-sentinel") {
			t.Fatalf("status=%d starts=%d unsafe=%t", status, starts, strings.Contains(out.String(), "failure-pat-sentinel"))
		}
	}
}
