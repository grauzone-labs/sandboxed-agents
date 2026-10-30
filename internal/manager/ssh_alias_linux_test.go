package manager

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// aliasEffects supplies a verified agent with a temporary home, scripted alias answers, and the real local ssh -G query at the process seam.
func aliasEffects(t *testing.T, home, answers string, queries *[][]string) Effects {
	t.Helper()
	if _, err := os.Stat("/usr/bin/ssh"); err != nil {
		t.Skip("the alias unit queries the local OpenSSH client")
	}
	return Effects{
		Stdin:         strings.NewReader(answers),
		HomeDirectory: func() (string, error) { return home, nil },
		Identity:      func() Identity { return agentIdentity },
		Groups:        func() ([]int, error) { return nil, nil },
		UIDs:          func() (int, int, int) { return 1000, 1000, 1000 },
		GIDs:          func() (int, int, int) { return 1000, 1000, 1000 },
		Start: func(p Process) error {
			if p.Program != "/usr/bin/ssh" || p.Identity != agentIdentity || len(p.Args) == 0 || p.Args[0] != "-G" {
				t.Fatalf("unexpected process: %+v", p)
			}
			*queries = append(*queries, p.Args)
			c := exec.Command(p.Program, p.Args...)
			c.Stdin, c.Stdout, c.Stderr = p.Stdin, p.Stdout, p.Stderr
			c.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "LC_ALL=C"}
			return c.Run()
		},
	}
}

// resolveAlias returns what the real OpenSSH client resolves for an alias from the given configuration file.
func resolveAlias(t *testing.T, config, alias string) map[string][]string {
	t.Helper()
	output, err := exec.Command("/usr/bin/ssh", "-G", "-F", config, alias).Output()
	if err != nil {
		t.Fatalf("ssh -G %s: %v", alias, err)
	}
	resolved := map[string][]string{}
	for _, line := range strings.Split(string(output), "\n") {
		if key, value, ok := strings.Cut(line, " "); ok {
			resolved[key] = append(resolved[key], value)
		}
	}
	return resolved
}

// assertAliasResolves checks that OpenSSH resolves the alias to the target, remote username, only the dedicated key, and IdentitiesOnly.
func assertAliasResolves(t *testing.T, home, alias, hostname, user string) {
	t.Helper()
	resolved := resolveAlias(t, filepath.Join(home, ".ssh", "config"), alias)
	key := filepath.Join(home, ".ssh", "id_ed25519_remote_test")
	if got := resolved["hostname"]; len(got) != 1 || got[0] != hostname {
		t.Errorf("hostname=%q want %q", got, hostname)
	}
	if got := resolved["user"]; len(got) != 1 || got[0] != user {
		t.Errorf("user=%q want %q", got, user)
	}
	if got := resolved["identityfile"]; len(got) != 1 || got[0] != key {
		t.Errorf("identityfile=%q want %q", got, key)
	}
	if got := resolved["identitiesonly"]; len(got) != 1 || got[0] != "yes" {
		t.Errorf("identitiesonly=%q want yes", got)
	}
}

// TestSSHAliasMachineNameWritesResolvableEntry proves a machine-name target becomes the alias without a question and its written entry resolves through the real ssh -G, at the configureSSHAlias seam with agent-identity queries.
func TestSSHAliasMachineNameWritesResolvableEntry(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	var queries [][]string
	var stderr bytes.Buffer
	alias, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, "", &queries))
	if err != nil {
		t.Fatalf("error=%v diagnostics=%q", err, stderr.String())
	}
	if alias != "build.example" {
		t.Fatalf("alias=%q", alias)
	}
	if strings.Contains(stderr.String(), "alias name") {
		t.Fatalf("machine name asked for an alias: %q", stderr.String())
	}
	if len(queries) == 0 {
		t.Fatal("no conflict query ran")
	}
	assertAliasResolves(t, home, "build.example", "build.example", "alice")
}

// TestSSHAliasCreatesMissingConfigurationAsAgent proves a missing .ssh directory and config file are created owned by the agent with modes 0700 and 0600, at the configureSSHAlias seam over a temporary home.
func TestSSHAliasCreatesMissingConfigurationAsAgent(t *testing.T) {
	home := t.TempDir()
	var queries [][]string
	var stderr bytes.Buffer
	if _, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, "", &queries)); err != nil {
		t.Fatalf("error=%v diagnostics=%q", err, stderr.String())
	}
	for path, mode := range map[string]os.FileMode{filepath.Join(home, ".ssh"): 0700, filepath.Join(home, ".ssh", "config"): 0600} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		stat := info.Sys().(*syscall.Stat_t)
		if info.Mode().Perm() != mode || int(stat.Uid) != os.Geteuid() || int(stat.Gid) != os.Getegid() {
			t.Fatalf("%s mode=%v owner=%d:%d", path, info.Mode(), stat.Uid, stat.Gid)
		}
	}
	assertAliasResolves(t, home, "build.example", "build.example", "alice")
}

// TestSSHAliasAddressTargetAsksForAlias proves IPv4 and IPv6 targets ask the user for an alias name and write an entry for that alias with the address as HostName, at the configureSSHAlias seam with scripted standard input.
func TestSSHAliasAddressTargetAsksForAlias(t *testing.T) {
	for target, alias := range map[string]string{"192.0.2.10": "lab", "2001:db8::1": "lab-v6"} {
		t.Run(target, func(t *testing.T) {
			home := t.TempDir()
			var queries [][]string
			var stderr bytes.Buffer
			got, err := configureSSHAlias(target, "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, alias+"\n", &queries))
			if err != nil {
				t.Fatalf("error=%v diagnostics=%q", err, stderr.String())
			}
			if got != alias || strings.Count(stderr.String(), "SSH alias name for "+target) != 1 {
				t.Fatalf("alias=%q diagnostics=%q", got, stderr.String())
			}
			assertAliasResolves(t, home, alias, target, "alice")
		})
	}
}

// TestSSHAliasPreservesExistingConfiguration proves existing entries, comments, and a missing final newline stay byte for byte ahead of the one appended entry, at the configureSSHAlias seam over a hand-written config file.
func TestSSHAliasPreservesExistingConfiguration(t *testing.T) {
	home := t.TempDir()
	existing := "# kept by hand\nHost other\n    HostName 198.51.100.7\n    User bob\n# trailing comment without newline"
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, ".ssh", "config")
	if err := os.WriteFile(config, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}
	var queries [][]string
	var stderr bytes.Buffer
	if _, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, "", &queries)); err != nil {
		t.Fatalf("error=%v diagnostics=%q", err, stderr.String())
	}
	want := existing + "\n\nHost build.example\n    HostName build.example\n    User alice\n    IdentityFile " + filepath.Join(home, ".ssh", "id_ed25519_remote_test") + "\n    IdentitiesOnly yes\n"
	if data, err := os.ReadFile(config); err != nil || string(data) != want {
		t.Fatalf("config=%q error=%v\nwant %q", data, err, want)
	}
	assertAliasResolves(t, home, "build.example", "build.example", "alice")
}

// TestSSHAliasConflictWritesNothingAndAsksAgain proves an alias already resolved by a hand-written entry or effective Host * options writes nothing, is reported, and asks for another alias name, at the configureSSHAlias seam with the real ssh -G comparison.
func TestSSHAliasConflictWritesNothingAndAsksAgain(t *testing.T) {
	for _, test := range []struct {
		name, target, existing, answers, alias string
	}{
		{"hand-written address alias", "192.0.2.10", "Host lab\n    HostName 203.0.113.9\n", "lab\nlab2\n", "lab2"},
		{"hand-written machine name", "build.example", "# hand-written\nHost build.example\n    Port 2222\n", "build2\n", "build2"},
		{"Host star options", "build.example", "Host *\n    User bob\n", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.Mkdir(filepath.Join(home, ".ssh"), 0700); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(home, ".ssh", "config")
			if err := os.WriteFile(config, []byte(test.existing), 0600); err != nil {
				t.Fatal(err)
			}
			var queries [][]string
			var stderr bytes.Buffer
			alias, err := configureSSHAlias(test.target, "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, test.answers, &queries))
			if !strings.Contains(stderr.String(), "already configured") || !strings.Contains(stderr.String(), "SSH alias name for "+test.target) {
				t.Fatalf("conflict not reported or no new question: %q", stderr.String())
			}
			data, readErr := os.ReadFile(config)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if test.alias == "" {
				if err == nil || string(data) != test.existing {
					t.Fatalf("alias=%q error=%v config=%q", alias, err, data)
				}
				return
			}
			want := test.existing + "\nHost " + test.alias + "\n    HostName " + test.target + "\n    User alice\n    IdentityFile " + filepath.Join(home, ".ssh", "id_ed25519_remote_test") + "\n    IdentitiesOnly yes\n"
			if err != nil || alias != test.alias || string(data) != want {
				t.Fatalf("alias=%q error=%v config=%q\nwant %q", alias, err, data, want)
			}
			assertAliasResolves(t, home, test.alias, test.target, "alice")
		})
	}
}

// TestSSHAliasFailingQueryWritesNothing proves a broken configuration that makes ssh -G fail is reported with OpenSSH's diagnostic and leaves the file unchanged, at the configureSSHAlias seam with the real query.
func TestSSHAliasFailingQueryWritesNothing(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, ".ssh", "config")
	existing := "Host lab\n    NoSuchOption yes\n"
	if err := os.WriteFile(config, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}
	var queries [][]string
	var stderr bytes.Buffer
	_, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, "", &queries))
	if err == nil || !strings.Contains(err.Error(), "SSH configuration query for build.example failed") || !strings.Contains(err.Error(), "Bad configuration option") {
		t.Fatalf("error=%v", err)
	}
	if data, err := os.ReadFile(config); err != nil || string(data) != existing {
		t.Fatalf("config=%q error=%v", data, err)
	}
}

// TestSSHAliasRefusesNonAgentBeforeHomeAccess proves container root is refused before home data is read or ssh -G starts, at the configureSSHAlias seam's verifyAgent identity check.
func TestSSHAliasRefusesNonAgentBeforeHomeAccess(t *testing.T) {
	var queries [][]string
	effects := aliasEffects(t, t.TempDir(), "", &queries)
	effects.UIDs = func() (int, int, int) { return 0, 0, 0 }
	effects.GIDs = func() (int, int, int) { return 0, 0, 0 }
	effects.HomeDirectory = func() (string, error) { t.Fatal("home data read before identity check"); return "", nil }
	var stderr bytes.Buffer
	if _, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, effects); err == nil || len(queries) != 0 {
		t.Fatalf("error=%v queries=%q", err, queries)
	}
	if !strings.Contains(stderr.String(), "UID and GID 1000") {
		t.Fatalf("diagnostics=%q", stderr.String())
	}
}

// TestSSHAliasRefusesInvalidAliasNames proves alias answers with spaces, patterns, a leading dash, or no text are refused and asked again before any write, at the configureSSHAlias seam with scripted standard input.
func TestSSHAliasRefusesInvalidAliasNames(t *testing.T) {
	home := t.TempDir()
	var queries [][]string
	var stderr bytes.Buffer
	answers := "lab ProxyCommand touch pwned\n*\n-oProxyCommand=x\n\nlab\n"
	alias, err := configureSSHAlias("192.0.2.10", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, answers, &queries))
	if err != nil || alias != "lab" {
		t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr.String())
	}
	if strings.Count(stderr.String(), "SSH alias name for 192.0.2.10") != 5 || strings.Count(stderr.String(), "invalid SSH alias name") != 4 {
		t.Fatalf("diagnostics=%q", stderr.String())
	}
	for _, query := range queries {
		if query[len(query)-1] != "lab" {
			t.Fatalf("invalid alias reached ssh -G: %q", query)
		}
	}
	data, err := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if err != nil || strings.Count(string(data), "Host ") != 1 || strings.Contains(string(data), "ProxyCommand") {
		t.Fatalf("config=%q error=%v", data, err)
	}
	assertAliasResolves(t, home, "lab", "192.0.2.10", "alice")
}

// TestSSHAliasEscapesTokensAndBackslashes proves a zoned IPv6 HostName and a username with a backslash resolve unchanged through the real ssh -G, at the configureSSHAlias seam's written entry.
func TestSSHAliasEscapesTokensAndBackslashes(t *testing.T) {
	home := t.TempDir()
	var queries [][]string
	var stderr bytes.Buffer
	if _, err := configureSSHAlias("fe80::1%eth0", `dom\alice`, "id_ed25519_remote_test", &stderr, aliasEffects(t, home, "linklocal\n", &queries)); err != nil {
		t.Fatalf("error=%v diagnostics=%q", err, stderr.String())
	}
	assertAliasResolves(t, home, "linklocal", "fe80::1%eth0", `dom\alice`)
}
