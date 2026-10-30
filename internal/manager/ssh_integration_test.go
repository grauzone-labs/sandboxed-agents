package manager_test

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// enableSSHIntegration exposes the staged SSH workflow only for manager boundary tests.
func enableSSHIntegration(t *testing.T) {
	t.Helper()
	old := catalog.Default.Integrations["ssh"]
	integration := old
	workflow := integration.ConfigWorkflows[""]
	workflow.Delivered = true
	integration.ConfigWorkflows = map[string]catalog.ConfigWorkflow{"": workflow}
	catalog.Default.Integrations["ssh"] = integration
	t.Cleanup(func() { catalog.Default.Integrations["ssh"] = old })
}

// sshKeyEffects supplies real temporary-home storage and local key generation at the manager process seam without SSH or network access.
func sshKeyEffects(t *testing.T, home string, calls *[]manager.Process) manager.Effects {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("key permissions exercise the Linux manager")
	}
	e := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	e.HomeDirectory = func() (string, error) { return home, nil }
	e.Start = func(p manager.Process) error {
		*calls = append(*calls, p)
		if p.Program != "/usr/bin/ssh-keygen" || p.Identity != (manager.Identity{UID: 1000, GID: 1000}) || p.EnvironmentPolicy == manager.AgentEnvironment {
			t.Fatalf("unexpected key process: %+v", p)
		}
		c := exec.Command(p.Program, p.Args...)
		c.Stdin = p.Stdin
		c.Stdout = p.Stdout
		c.Stderr = p.Stderr
		c.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "LC_ALL=C", "SSH_ASKPASS_REQUIRE=never"}
		return c.Run()
	}
	return e
}

// runSSHKey invokes the SSH workflow with separate public-output and diagnostic streams; without terminal input, the workflow
// ends at the approval question that follows the key stage.
func runSSHKey(e manager.Effects, host, user string) (int, string, string) {
	var out, err bytes.Buffer
	status := manager.RunWithEffects([]string{"integrations", "config", "ssh", "--host", host, "--user", user}, &out, &err, e)
	return status, out.String(), err.String()
}

// keyStageDone reports whether a runSSHKey run completed the key stage: it reached the approval question, which end of input
// cancels with status 1 before any connection.
func keyStageDone(status int, diag string) bool {
	return status == 1 && strings.Contains(diag, "SSH connection test cancelled; no connection made")
}

// TestSSHIntegrationGeneratesPrivateKeyAndPrintsPublic proves agent-only local key generation, protected files, and exactly one public line through RunWithEffects and real temporary-home storage.
func TestSSHIntegrationGeneratesPrivateKeyAndPrintsPublic(t *testing.T) {
	enableSSHIntegration(t)
	home := t.TempDir()
	var calls []manager.Process
	status, out, diag := runSSHKey(sshKeyEffects(t, home, &calls), "2001:db8::1", "alice")
	if !keyStageDone(status, diag) {
		t.Fatalf("status=%d diagnostics=%s", status, diag)
	}
	keys, err := filepath.Glob(filepath.Join(home, ".ssh", "id_ed25519_remote_*"))
	if err != nil || len(keys) != 2 {
		t.Fatalf("files=%q error=%v", keys, err)
	}
	var private string
	for _, p := range keys {
		if !strings.HasSuffix(p, ".pub") {
			private = p
		}
	}
	if private == "" {
		t.Fatal("missing private key")
	}
	if filepath.Base(private) != "id_ed25519_remote_5afd19e856d1c18d17d600dfd2b5f534992333985e126c2a951047102c1ed536" {
		t.Fatalf("unexpected normalized target filename %q", private)
	}
	for path, mode := range map[string]os.FileMode{filepath.Dir(private): 0700, private: 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("path=%s info=%v error=%v", path, info, err)
		}
	}
	public, err := os.ReadFile(private + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(out)
	if len(fields) != 2 || fields[0] != "ssh-ed25519" || strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("invalid public output %q", out)
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil || len(blob) != 51 {
		t.Fatalf("invalid public encoding %q", out)
	}
	if !strings.HasPrefix(string(public), strings.TrimSuffix(out, "\n")) {
		t.Fatal("printed key differs from generated public key")
	}
	secret, err := os.ReadFile(private)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(out+diag), secret) || strings.Contains(out+diag, "PRIVATE KEY") {
		t.Fatal("private key leaked")
	}
	if !strings.Contains(diag, private) {
		t.Fatalf("missing retained key location in %q", diag)
	}
	for _, p := range calls {
		if strings.Contains(fmt.Sprint(p.Args), "PRIVATE KEY") {
			t.Fatal("private key in argv")
		}
	}
	entries, err := os.ReadDir(filepath.Join(home, ".ssh"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("unexpected config, trust or temporary files: %v %v", entries, err)
	}
}

// TestSSHIntegrationReusesNormalizedMachineKey proves equivalent IPv6 spellings, hostname case, and different usernames reuse the same valid identity through RunWithEffects and local keygen.
func TestSSHIntegrationReusesNormalizedMachineKey(t *testing.T) {
	enableSSHIntegration(t)
	for _, hosts := range [][2]string{{"2001:0DB8:0:0:0:0:0:1", "2001:db8::1"}, {"Build.Example", "build.example"}} {
		t.Run(hosts[0], func(t *testing.T) {
			home := t.TempDir()
			var calls []manager.Process
			e := sshKeyEffects(t, home, &calls)
			status, first, diag := runSSHKey(e, hosts[0], "alice")
			if !keyStageDone(status, diag) {
				t.Fatalf("first status=%d %s", status, diag)
			}
			paths, _ := filepath.Glob(filepath.Join(home, ".ssh", "id_ed25519_remote_*"))
			before := map[string][]byte{}
			for _, p := range paths {
				data, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				before[p] = data
			}
			status, second, diag := runSSHKey(e, hosts[1], "bob")
			if !keyStageDone(status, diag) || second != first {
				t.Fatalf("retry status=%d output=%q want=%q diagnostics=%s", status, second, first, diag)
			}
			generations := 0
			for _, p := range calls {
				if slices.Contains(p.Args, "-t") {
					generations++
				}
			}
			if generations != 1 {
				t.Fatalf("generations=%d", generations)
			}
			for p, want := range before {
				got, err := os.ReadFile(p)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("key changed: %s", p)
				}
			}
		})
	}
}

// TestSSHIntegrationRefusesDamagedOrIncompleteKeys proves malformed private material, mismatched pairs, unsafe paths, and incomplete pairs are preserved and refused through RunWithEffects.
func TestSSHIntegrationRefusesDamagedOrIncompleteKeys(t *testing.T) {
	enableSSHIntegration(t)
	for _, kind := range []string{"private-corrupt", "public-corrupt", "public-mismatch", "private-missing", "public-missing", "private-readable", "private-symlink", "public-symlink"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			var calls []manager.Process
			e := sshKeyEffects(t, home, &calls)
			status, _, diag := runSSHKey(e, "worker", "alice")
			if !keyStageDone(status, diag) {
				t.Fatalf("setup status=%d %s", status, diag)
			}
			matches, _ := filepath.Glob(filepath.Join(home, ".ssh", "id_ed25519_remote_*"))
			key := strings.TrimSuffix(matches[0], ".pub")
			switch kind {
			case "private-corrupt":
				if err := os.WriteFile(key, []byte("PRIVATE KEY sentinel\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "public-corrupt":
				if err := os.WriteFile(key+".pub", []byte("PRIVATE KEY sentinel\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "public-mismatch":
				status, other, diag := runSSHKey(e, "other", "alice")
				if !keyStageDone(status, diag) {
					t.Fatalf("other key status=%d %s", status, diag)
				}
				if err := os.WriteFile(key+".pub", []byte(other), 0600); err != nil {
					t.Fatal(err)
				}
			case "private-missing":
				if err := os.Remove(key); err != nil {
					t.Fatal(err)
				}
			case "public-missing":
				if err := os.Remove(key + ".pub"); err != nil {
					t.Fatal(err)
				}
			case "private-readable":
				if err := os.Chmod(key, 0644); err != nil {
					t.Fatal(err)
				}
			case "private-symlink", "public-symlink":
				path := key
				if kind == "public-symlink" {
					path += ".pub"
				}
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".saved", path); err != nil {
					t.Fatal(err)
				}
			}
			before := map[string][]byte{}
			for _, path := range []string{key, key + ".pub"} {
				if data, err := os.ReadFile(path); err == nil {
					before[path] = data
				}
			}
			calls = nil
			status, out, diag := runSSHKey(e, "worker", "bob")
			if status != 1 || out != "" || strings.Contains(diag, "PRIVATE KEY sentinel") || strings.Contains(diag, "SSH connection test") {
				t.Fatalf("status=%d output=%q diagnostics=%q", status, out, diag)
			}
			for _, p := range calls {
				if slices.Contains(p.Args, "-t") {
					t.Fatal("attempted to overwrite key")
				}
			}
			for path, want := range before {
				data, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(data, want) {
					t.Fatalf("changed %s", path)
				}
			}
		})
	}
}

// TestSSHIntegrationSuppressesUntrustedDiagnosticsAndRetainsKeys proves process errors and output cannot disclose key material and failure retains reusable keys through RunWithEffects.
func TestSSHIntegrationSuppressesUntrustedDiagnosticsAndRetainsKeys(t *testing.T) {
	enableSSHIntegration(t)
	for _, phase := range []string{"generation", "validation", "invalid-public-output", "oversized-public-output"} {
		t.Run(phase, func(t *testing.T) {
			home := t.TempDir()
			var calls []manager.Process
			e := sshKeyEffects(t, home, &calls)
			start := e.Start
			e.Start = func(p manager.Process) error {
				if slices.Contains(p.Args, "-y") && (phase == "invalid-public-output" || phase == "oversized-public-output") {
					secret := "-----BEGIN OPENSSH PRIVATE KEY-----\nprivate sentinel\n"
					if phase == "oversized-public-output" {
						secret = strings.Repeat(secret, 1000)
					}
					fmt.Fprint(p.Stdout, secret)
					fmt.Fprint(p.Stderr, secret)
					return nil
				}
				err := start(p)
				if err != nil {
					return err
				}
				if (phase == "generation" && slices.Contains(p.Args, "-t")) || (phase == "validation" && slices.Contains(p.Args, "-y")) {
					paths, _ := filepath.Glob(filepath.Join(home, ".ssh", "id_ed25519_remote_*"))
					secret, err := os.ReadFile(strings.TrimSuffix(paths[0], ".pub"))
					if err != nil {
						t.Fatal(err)
					}
					fmt.Fprint(p.Stdout, string(secret))
					fmt.Fprint(p.Stderr, string(secret))
					return fmt.Errorf("untrusted process error: %s", secret)
				}
				return nil
			}
			status, out, diag := runSSHKey(e, "worker", "alice")
			if status != 1 || out != "" || strings.Contains(diag, "PRIVATE KEY") || strings.Contains(diag, "private sentinel") || !strings.Contains(diag, "Dedicated SSH key location:") || strings.Contains(diag, "SSH connection test") {
				t.Fatalf("status=%d output=%q diagnostics=%q", status, out, diag)
			}
			paths, _ := filepath.Glob(filepath.Join(home, ".ssh", "id_ed25519_remote_*"))
			if len(paths) != 2 {
				t.Fatalf("generated material not retained: %q", paths)
			}
			e.Start = start
			calls = nil
			status, out, diag = runSSHKey(e, "worker", "alice")
			if !keyStageDone(status, diag) || !strings.HasPrefix(out, "ssh-ed25519 ") {
				t.Fatalf("retry status=%d diagnostics=%s", status, diag)
			}
			for _, p := range calls {
				if slices.Contains(p.Args, "-t") {
					t.Fatal("retry regenerated a retained key")
				}
			}
		})
	}
}

// TestSSHIntegrationUndeliveredAndMalformedRequestsHaveNoEffects proves the default gate and direct manager input validation precede all home and process access through RunWithEffects.
func TestSSHIntegrationUndeliveredAndMalformedRequestsHaveNoEffects(t *testing.T) {
	e := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	e.HomeDirectory = func() (string, error) { t.Fatal("accessed home for rejected command"); return "", nil }
	status, out, diag := runSSHKey(e, "worker", "alice")
	if status != 2 || out != "" || !strings.Contains(diag, `unknown integration "ssh"`) {
		t.Fatalf("status=%d out=%q diagnostics=%s", status, out, diag)
	}
	enableSSHIntegration(t)
	for _, args := range [][]string{{"--host", "worker"}, {"--user", "alice"}, {"--host", "bad\nhost", "--user", "alice"}, {"--host", "worker", "--user", "bad\nuser"}, {"connect", "--host", "worker", "--user", "alice"}, {"--secret", "PRIVATE KEY sentinel"}} {
		var out, diag bytes.Buffer
		status := manager.RunWithEffects(append([]string{"integrations", "config", "ssh"}, args...), &out, &diag, e)
		if status != 2 || out.Len() != 0 || strings.Contains(diag.String(), "PRIVATE KEY sentinel") {
			t.Fatalf("status=%d output=%q diagnostics=%q", status, &out, &diag)
		}
	}
}

// TestSSHIntegrationRefusesUnsafeDirectory proves the manager never follows a redirected SSH directory or changes its existing permissions through RunWithEffects and real temporary storage.
func TestSSHIntegrationRefusesUnsafeDirectory(t *testing.T) {
	enableSSHIntegration(t)
	for _, kind := range []string{"symlink", "file", "permissive"} {
		t.Run(kind, func(t *testing.T) {
			if runtime.GOOS != "linux" {
				t.Skip("Linux storage permissions")
			}
			home, outside := t.TempDir(), t.TempDir()
			path := filepath.Join(home, ".ssh")
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(outside, path)
			case "file":
				err = os.WriteFile(path, []byte("keep"), 0600)
			case "permissive":
				err = os.Mkdir(path, 0755)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			e := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
			e.HomeDirectory = func() (string, error) { return home, nil }
			status, out, diag := runSSHKey(e, "worker", "alice")
			if status != 1 || out != "" {
				t.Fatalf("status=%d out=%q diagnostics=%q", status, out, diag)
			}
			after, err := os.Lstat(path)
			if err != nil || after.Mode() != before.Mode() {
				t.Fatal("changed preexisting SSH path")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("touched outside directory: %v %v", entries, err)
			}
		})
	}
}
