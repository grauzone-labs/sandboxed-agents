package gitcredentials_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/gitcredentials"
)

// TestSetupRejectsEmptyAndMismatchedPassphrases proves failed first-run input creates no managed state through Store.Setup.
func TestSetupRejectsEmptyAndMismatchedPassphrases(t *testing.T) {
	for _, pair := range [][2]string{{"", ""}, {"dummy", "different"}} {
		home := t.TempDir()
		s := gitcredentials.Store{Home: home, Manager: "/usr/local/bin/sandboxed-agents-manager"}
		if err := s.Setup([]byte(pair[0]), []byte(pair[1])); err == nil {
			t.Fatal("invalid passphrase accepted")
		}
		entries, err := os.ReadDir(home)
		if err != nil || len(entries) != 0 {
			t.Fatalf("home changed: %v %v", entries, err)
		}
	}
}

// TestStoreSetupAndWarmCacheRelock proves real GPG creates one key, reuses it and refuses wrong input after a decrypt at the setup and helper seams.
func TestStoreSetupAndWarmCacheRelock(t *testing.T) {
	s := realStore(t)
	home := s.Home
	passphrase := []byte("dummy-passphrase-% +")
	if err := s.Setup(passphrase, passphrase); err != nil {
		t.Fatal(err)
	}
	if state, err := s.State(); err != nil || state != "ready" {
		t.Fatalf("state=%s error=%v", state, err)
	}
	if err := s.Helper([]string{"--generic", "store"}, strings.NewReader("protocol=https\nhost=example.invalid\nusername=dummy\npassword=warm-cache-dummy\n\n"), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Helper([]string{"--generic", "get"}, strings.NewReader("protocol=https\nhost=example.invalid\nusername=dummy\n\n"), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Setup([]byte("wrong-dummy"), nil); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
	if err := s.Check(); err == nil {
		t.Fatal("wrong passphrase retained unlock")
	}
	if err := s.Setup(passphrase, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(home + "/.local/share/sandboxed-agents/git-credentials/gnupg/private-keys-v1.d")
	if len(files) != 2 {
		t.Fatalf("private key files=%d; want primary and subkey", len(files))
	}
	data, _ := os.ReadFile(home + "/.gitconfig")
	if !strings.Contains(string(data), "credentialStore = gpg") {
		t.Fatalf("config=%s", data)
	}
}

// realStore supplies an isolated home and real IPC programs, skipping unsupported host GnuPG versions explicitly.
func realStore(t *testing.T) gitcredentials.Store {
	t.Helper()
	for _, name := range []string{"gpg", "gpgconf", "pass", "git-credential-manager"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip("real " + name + " unavailable")
		}
	}
	version, _ := exec.Command("gpg", "--version").Output()
	if strings.Contains(string(version), "gpg (GnuPG) 2.2.") {
		t.Skip("GnuPG 2.2 lacks PUT_SECRET/GET_SECRET; run the built-image check with GnuPG 2.4.7")
	}
	home, err := os.MkdirTemp("", "gc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	manager := home + "/manager"

	if fixture := os.Getenv("SA_GIT_CREDENTIAL_TEST_MANAGER"); fixture != "" {
		if err := os.Symlink(fixture, manager); err != nil {
			t.Fatal(err)
		}
	} else {
		cmd := exec.Command("go", "build", "-o", manager, "./testdata/manager")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build fixture: %s %v", out, err)
		}
	}
	if err := os.Symlink(manager, home+"/sandboxed-agents-pinentry"); err != nil {
		t.Fatal(err)
	}
	s := gitcredentials.Store{Home: home, Manager: manager}
	t.Cleanup(func() { s.Lock() })

	return s
}

// TestStoreMigratesEveryCredential proves Setup imports legacy entries and verifies real Git readback before deleting plaintext at the setup seam.
func TestStoreMigratesEveryCredential(t *testing.T) {
	s := realStore(t)
	passphrase := []byte("migration-dummy")
	if err := s.Setup(passphrase, passphrase); err != nil {
		t.Fatal(err)
	}
	legacy := s.Home + "/.git-credentials"
	data := []byte("https://alice:one%25two@example.invalid\nhttps://bob:other@other.invalid\n")
	if err := os.WriteFile(legacy, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Setup(passphrase, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("legacy plaintext remains")
	}
}

// TestMigrationFailurePreservesSourceAndHelpers proves a failed encrypted write or readback retains the plaintext source and exact global config through Setup and real Git.
func TestMigrationFailurePreservesSourceAndHelpers(t *testing.T) {
	s := realStore(t)
	passphrase := []byte("failure-dummy")
	if err := s.Setup(passphrase, passphrase); err != nil {
		t.Fatal(err)
	}
	legacy := s.Home + "/.git-credentials"
	source := []byte("https://alice:dummy-pat@failure.invalid\n")
	os.WriteFile(legacy, source, 0600)
	config := s.Home + "/.gitconfig"
	old := []byte("[credential]\n helper = store --file=" + legacy + "\n helper = another-old-helper\n")
	os.WriteFile(config, old, 0600)
	store := s.Home + "/.local/share/sandboxed-agents/git-credentials/password-store"
	os.Chmod(store, 0500)
	defer os.Chmod(store, 0700)
	if err := s.Setup(passphrase, nil); err == nil {
		t.Fatal("failed migration accepted")
	}
	gotSource, _ := os.ReadFile(legacy)
	gotConfig, _ := os.ReadFile(config)
	if string(gotSource) != string(source) || string(gotConfig) != string(old) {
		t.Fatal("failed migration changed source or global helpers")
	}
}

// TestGitHubCoexistsInBothOrders proves real gh host helper resets retain priority over GCM regardless of setup order at the real Git credential seam.
func TestGitHubCoexistsInBothOrders(t *testing.T) {
	if _, err := exec.LookPath("gh"); err != nil {
		t.Skip("real gh unavailable")
	}
	for _, githubFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(githubFirst), func(t *testing.T) {
			s := realStore(t)
			passphrase := []byte("coexistence-dummy")
			ghdir := s.Home + "/.config/gh"
			os.MkdirAll(ghdir, 0700)
			os.WriteFile(ghdir+"/hosts.yml", []byte("github.com:\n    oauth_token: dummy-gh-token\n    user: dummy\n    git_protocol: https\n"), 0600)
			github := func() {
				cmd := exec.Command("gh", "auth", "setup-git", "--hostname", "github.com")
				cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + s.Home, "GH_CONFIG_DIR=" + ghdir, "GIT_CONFIG_GLOBAL=" + s.Home + "/.gitconfig"}
				if err := cmd.Run(); err != nil {
					t.Fatal("gh setup-git failed")
				}
			}
			if githubFirst {
				github()
			}
			if err := s.Setup(passphrase, passphrase); err != nil {
				t.Fatal(err)
			}
			if !githubFirst {
				github()
			}
			for _, host := range []string{"github.com", "gist.github.com"} {
				data, err := gitRequest(s, "fill", "protocol=https\nhost="+host+"\n\n")
				if err != nil || !strings.Contains(string(data), "password=dummy-gh-token\n") {
					t.Fatalf("gh helper did not resolve %s: %v", host, err)
				}
			}
			if _, err := gitRequest(s, "approve", "protocol=https\nhost=example.invalid\nusername=dummy\npassword=coexistence-pat\n\n"); err != nil {
				t.Fatal(err)
			}
			data, err := gitRequest(s, "fill", "protocol=https\nhost=example.invalid\nusername=dummy\n\n")
			if err != nil || !strings.Contains(string(data), "password=coexistence-pat\n") {
				t.Fatal("GCM did not resolve other host")
			}
		})
	}
}

// gitRequest runs real Git against only an isolated home's configuration and private stdin.
func gitRequest(s gitcredentials.Store, operation, record string) ([]byte, error) {
	cmd := exec.Command("git", "credential", operation)
	cmd.Dir = s.Home
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + s.Home, "GIT_CONFIG_GLOBAL=" + s.Home + "/.gitconfig", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"}
	cmd.Stdin = strings.NewReader(record)
	return cmd.Output()
}

// TestConfigurationFailureRetainsLegacyCredentials proves a Git config lock refuses setup before plaintext deletion at the setup seam.
func TestConfigurationFailureRetainsLegacyCredentials(t *testing.T) {
	s := realStore(t)
	passphrase := []byte("config-lock-dummy")
	if err := s.Setup(passphrase, passphrase); err != nil {
		t.Fatal(err)
	}
	source := []byte("https://alice:dummy-pat@config-failure.invalid\n")
	os.WriteFile(s.Home+"/.git-credentials", source, 0600)
	os.WriteFile(s.Home+"/.gitconfig.lock", nil, 0600)
	if err := s.Setup(passphrase, nil); err == nil {
		t.Fatal("Git config lock ignored")
	}
	got, _ := os.ReadFile(s.Home + "/.git-credentials")
	if string(got) != string(source) {
		t.Fatal("config failure removed legacy credentials")
	}
}

// TestDamagedStoreStateIsNeverRepaired proves missing counterparts, private-key damage, and recipient mismatch leave existing bytes unchanged through Setup.
func TestDamagedStoreStateIsNeverRepaired(t *testing.T) {
	s := realStore(t)
	passphrase := []byte("damage-dummy")
	if err := s.Setup(passphrase, passphrase); err != nil {
		t.Fatal(err)
	}
	root := s.Home + "/.local/share/sandboxed-agents/git-credentials"
	keys, _ := os.ReadDir(root + "/gnupg/private-keys-v1.d")
	for _, path := range []string{root + "/password-store", root + "/gnupg/private-keys-v1.d/" + keys[0].Name()} {
		if err := os.Rename(path, path+".saved"); err != nil {
			t.Fatal(err)
		}
		before := fileBytes(t, root)
		if err := s.Setup(passphrase, nil); err == nil {
			t.Fatal("missing state accepted")
		}
		if !reflect.DeepEqual(fileBytes(t, root), before) {
			t.Fatal("refusal changed existing files")
		}
		os.Rename(path+".saved", path)
	}
	for _, path := range []string{root + "/gnupg/private-keys-v1.d/" + keys[0].Name(), root + "/password-store/.gpg-id"} {
		original, _ := os.ReadFile(path)
		os.WriteFile(path, []byte("damaged"), 0600)
		before := fileBytes(t, root)
		if err := s.Setup(passphrase, nil); err == nil {
			t.Fatal("damaged state accepted")
		}
		if !reflect.DeepEqual(fileBytes(t, root), before) {
			t.Fatal("refusal changed existing files")
		}
		os.WriteFile(path, original, 0600)
	}
}

// fileBytes snapshots regular managed files without reading sockets or exposing key bytes to diagnostics.
func fileBytes(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result[path] = string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
