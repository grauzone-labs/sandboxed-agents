package manager

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// sshQuery is one ssh -G request: the configuration file argument and the alias.
type sshQuery struct{ config, alias string }

// unconfiguredSSH is the client default an ssh -G query reports for an alias that no configuration touches.
func unconfiguredSSH(alias string) string {
	return "host " + alias + "\nuser agent\nhostname " + alias + "\nport 22\nidentitiesonly no\nserveraliveinterval 0\nidentityfile ~/.ssh/id_rsa\nidentityfile ~/.ssh/id_ed25519\nuserknownhostsfile /home/agent/.ssh/known_hosts /home/agent/.ssh/known_hosts2\n"
}

// unconfigured answers every scripted ssh -G query with the client default.
func unconfigured(q sshQuery) (string, error) { return unconfiguredSSH(q.alias), nil }

// aliasEffects supplies a verified agent with a temporary home and stdin, and answers ssh -G at the process seam from a script,
// checking that each query runs the trusted client as agent with the fixed SSH environment.
func aliasEffects(t *testing.T, home string, stdin io.Reader, resolve func(sshQuery) (string, error), queries *[]sshQuery) Effects {
	t.Helper()
	return Effects{
		Stdin:         stdin,
		HomeDirectory: func() (string, error) { return home, nil },
		Identity:      func() Identity { return agentIdentity },
		Groups:        func() ([]int, error) { return nil, nil },
		UIDs:          func() (int, int, int) { return 1000, 1000, 1000 },
		GIDs:          func() (int, int, int) { return 1000, 1000, 1000 },
		Start: func(p Process) error {
			if p.Program != "/usr/bin/ssh" || p.Identity != agentIdentity || p.EnvironmentPolicy != SSHEnvironment || len(p.Args) != 4 || p.Args[0] != "-G" || p.Args[1] != "-F" {
				t.Fatalf("unexpected process: %+v", p)
			}
			q := sshQuery{config: p.Args[2], alias: p.Args[3]}
			if queries != nil {
				*queries = append(*queries, q)
			}
			output, err := resolve(q)
			if err != nil {
				fmt.Fprintln(p.Stderr, err)
				return errors.New("exit status 255")
			}
			_, err = io.WriteString(p.Stdout, output)
			return err
		},
	}
}

// aliasEntry is the host entry #111 specifies, in the layout the configuration file receives.
func aliasEntry(alias, hostName, user, identityFile string) string {
	return "Host " + alias + "\n    HostName " + hostName + "\n    User " + user + "\n    IdentityFile " + identityFile + "\n    IdentitiesOnly yes\n"
}

// writeSSHConfig creates the agent's .ssh directory and a configuration file with the given bytes and mode.
func writeSSHConfig(t *testing.T, home, content string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, ".ssh", "config")
	if err := os.WriteFile(config, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(config, mode); err != nil {
		t.Fatal(err)
	}
	return config
}

// readConfig returns the configuration file bytes, failing the test when it cannot be read.
func readConfig(t *testing.T, config string) string {
	t.Helper()
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestSSHAliasMachineNameUsesTargetWithoutQuestion proves a machine-name target becomes the alias without a question, its conflict query runs as agent, and the entry is written, at the configureSSHAlias seam with scripted ssh -G.
func TestSSHAliasMachineNameUsesTargetWithoutQuestion(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	var queries []sshQuery
	var stderr bytes.Buffer
	alias, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(""), unconfigured, &queries))
	if err != nil || alias != "build.example" {
		t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr.String())
	}
	if strings.Contains(stderr.String(), "alias name") {
		t.Fatalf("machine name asked for an alias: %q", stderr.String())
	}
	if len(queries) == 0 || queries[0].alias != "build.example" {
		t.Fatalf("queries=%v", queries)
	}
	want := aliasEntry("build.example", "build.example", "alice", filepath.Join(home, ".ssh", "id_ed25519_remote_test"))
	if got := readConfig(t, filepath.Join(home, ".ssh", "config")); got != want {
		t.Fatalf("config=%q\nwant %q", got, want)
	}
}

// TestSSHAliasCreatesMissingConfigurationAsAgent proves a missing .ssh directory and config file are created owned by the agent with modes 0700 and 0600, at the configureSSHAlias seam over a temporary home.
func TestSSHAliasCreatesMissingConfigurationAsAgent(t *testing.T) {
	home := t.TempDir()
	var stderr bytes.Buffer
	if _, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(""), unconfigured, nil)); err != nil {
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
}

// TestSSHAliasAddressTargetAsksForAlias proves IPv4 and IPv6 targets ask the user for an alias name and write an entry for that alias with the address as HostName, at the configureSSHAlias seam with scripted standard input.
func TestSSHAliasAddressTargetAsksForAlias(t *testing.T) {
	for target, alias := range map[string]string{"192.0.2.10": "lab", "2001:db8::1": "lab-v6"} {
		t.Run(target, func(t *testing.T) {
			home := t.TempDir()
			var stderr bytes.Buffer
			got, err := configureSSHAlias(target, "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(alias+"\n"), unconfigured, nil))
			if err != nil || got != alias || strings.Count(stderr.String(), "SSH alias name for "+target) != 1 {
				t.Fatalf("alias=%q error=%v diagnostics=%q", got, err, stderr.String())
			}
			want := aliasEntry(alias, target, "alice", filepath.Join(home, ".ssh", "id_ed25519_remote_test"))
			if config := readConfig(t, filepath.Join(home, ".ssh", "config")); config != want {
				t.Fatalf("config=%q\nwant %q", config, want)
			}
		})
	}
}

// TestSSHAliasPreservesExistingConfiguration proves existing entries, comments, and a missing final newline stay byte for byte ahead of the one appended entry, at the configureSSHAlias seam over a hand-written config file.
func TestSSHAliasPreservesExistingConfiguration(t *testing.T) {
	home := t.TempDir()
	existing := "# kept by hand\nHost other\n    HostName 198.51.100.7\n    User bob\n# trailing comment without newline"
	config := writeSSHConfig(t, home, existing, 0600)
	var stderr bytes.Buffer
	if _, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(""), unconfigured, nil)); err != nil {
		t.Fatalf("error=%v diagnostics=%q", err, stderr.String())
	}
	want := existing + "\n\n" + aliasEntry("build.example", "build.example", "alice", filepath.Join(home, ".ssh", "id_ed25519_remote_test"))
	if got := readConfig(t, config); got != want {
		t.Fatalf("config=%q\nwant %q", got, want)
	}
}

// TestSSHAliasConflictWritesNothingAndAsksAgain proves an alias whose configured hostname, user, identity file, or IdentitiesOnly differs from the client default writes nothing, is reported, and asks for another alias name, at the configureSSHAlias seam with scripted ssh -G.
func TestSSHAliasConflictWritesNothingAndAsksAgain(t *testing.T) {
	for _, test := range []struct {
		name, target, answers, conflicting, line, alias string
	}{
		{"hand-written address alias", "192.0.2.10", "lab\nlab2\n", "lab", "hostname 203.0.113.9", "lab2"},
		{"hand-written machine name", "build.example", "build2\n", "build.example", "user bob", "build2"},
		{"Host star identity file", "build.example", "build2\n", "build.example", "identityfile ~/.ssh/shared", "build2"},
		{"Host star IdentitiesOnly", "192.0.2.10", "lab\nlab2\n", "lab", "identitiesonly yes", "lab2"},
		{"Host star user for every alias", "build.example", "", "*", "user bob", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			existing := "# hand-written\n"
			config := writeSSHConfig(t, home, existing, 0600)
			resolve := func(q sshQuery) (string, error) {
				output := unconfiguredSSH(q.alias)
				if q.config == config && (q.alias == test.conflicting || test.conflicting == "*") {
					key, _, _ := strings.Cut(test.line, " ")
					var lines []string
					for _, line := range strings.SplitAfter(output, "\n") {
						if !strings.HasPrefix(line, key+" ") || key == "identityfile" {
							lines = append(lines, line)
						}
					}
					output = strings.Join(lines, "") + test.line + "\n"
				}
				return output, nil
			}
			var stderr bytes.Buffer
			alias, err := configureSSHAlias(test.target, "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(test.answers), resolve, nil))
			if !strings.Contains(stderr.String(), "already configured") || !strings.Contains(stderr.String(), "SSH alias name for "+test.target) {
				t.Fatalf("conflict not reported or no new question: %q", stderr.String())
			}
			got := readConfig(t, config)
			if test.alias == "" {
				if err == nil || got != existing {
					t.Fatalf("alias=%q error=%v config=%q", alias, err, got)
				}
				return
			}
			want := existing + "\n" + aliasEntry(test.alias, test.target, "alice", filepath.Join(home, ".ssh", "id_ed25519_remote_test"))
			if err != nil || alias != test.alias || got != want {
				t.Fatalf("alias=%q error=%v config=%q\nwant %q", alias, err, got, want)
			}
		})
	}
}

// TestSSHAliasReplacesConfigurationAtomically proves the entry reaches ~/.ssh/config by replacing the file in one rename: a reader of the
// previous file still sees exactly the previous bytes, the new file keeps the previous mode, and no temporary file remains, at the configureSSHAlias seam.
func TestSSHAliasReplacesConfigurationAtomically(t *testing.T) {
	home := t.TempDir()
	existing := "# kept by hand\nHost other\n    User bob\n"
	config := writeSSHConfig(t, home, existing, 0644)
	previous, err := os.Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close()
	var stderr bytes.Buffer
	if _, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(""), unconfigured, nil)); err != nil {
		t.Fatalf("error=%v diagnostics=%q", err, stderr.String())
	}
	if old, err := io.ReadAll(previous); err != nil || string(old) != existing {
		t.Fatalf("previous file changed in place: %q %v", old, err)
	}
	want := existing + "\n" + aliasEntry("build.example", "build.example", "alice", filepath.Join(home, ".ssh", "id_ed25519_remote_test"))
	if got := readConfig(t, config); got != want {
		t.Fatalf("config=%q\nwant %q", got, want)
	}
	if info, err := os.Stat(config); err != nil || info.Mode().Perm() != 0644 {
		t.Fatalf("config mode=%v error=%v", info, err)
	}
	if entries, err := os.ReadDir(filepath.Join(home, ".ssh")); err != nil || len(entries) != 1 {
		t.Fatalf("leftover files=%v error=%v", entries, err)
	}
}

// TestSSHAliasIgnoresUnrelatedEffectiveOptions proves Host * or hand-written options outside hostname, user, identityfile, and identitiesonly,
// such as ServerAliveInterval or Port, do not block the alias, at the configureSSHAlias seam with scripted ssh -G.
func TestSSHAliasIgnoresUnrelatedEffectiveOptions(t *testing.T) {
	home := t.TempDir()
	existing := "Host *\n    ServerAliveInterval 30\n    Port 2222\n"
	config := writeSSHConfig(t, home, existing, 0600)
	resolve := func(q sshQuery) (string, error) {
		output := unconfiguredSSH(q.alias)
		if q.config == config {
			output = strings.NewReplacer("port 22\n", "port 2222\n", "serveraliveinterval 0\n", "serveraliveinterval 30\n").Replace(output)
		}
		return output, nil
	}
	var stderr bytes.Buffer
	alias, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(""), resolve, nil))
	if err != nil || alias != "build.example" || strings.Contains(stderr.String(), "already configured") {
		t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr.String())
	}
	want := existing + "\n" + aliasEntry("build.example", "build.example", "alice", filepath.Join(home, ".ssh", "id_ed25519_remote_test"))
	if got := readConfig(t, config); got != want {
		t.Fatalf("config=%q\nwant %q", got, want)
	}
}

// TestSSHAliasLeavesLaterInputUnread proves the alias questions consume exactly their answer lines, across a conflict and a second question,
// so a later answer for the caller stays readable on the same standard input, at the configureSSHAlias seam with scripted stdin.
func TestSSHAliasLeavesLaterInputUnread(t *testing.T) {
	home := t.TempDir()
	config := writeSSHConfig(t, home, "Host lab\n    HostName 203.0.113.9\n", 0600)
	resolve := func(q sshQuery) (string, error) {
		if q.config == config && q.alias == "lab" {
			return strings.Replace(unconfiguredSSH(q.alias), "hostname lab\n", "hostname 203.0.113.9\n", 1), nil
		}
		return unconfiguredSSH(q.alias), nil
	}
	stdin := strings.NewReader("lab\nlab2\nlater answer\n")
	var stderr bytes.Buffer
	if alias, err := configureSSHAlias("192.0.2.10", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, stdin, resolve, nil)); err != nil || alias != "lab2" {
		t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr.String())
	}
	if rest, err := io.ReadAll(stdin); err != nil || string(rest) != "later answer\n" {
		t.Fatalf("remaining input=%q error=%v", rest, err)
	}
}

// TestSSHAliasFailingQueryWritesNothing proves a failing ssh -G query is reported with OpenSSH's diagnostic and leaves the file unchanged, at the configureSSHAlias seam with a scripted query failure.
func TestSSHAliasFailingQueryWritesNothing(t *testing.T) {
	home := t.TempDir()
	existing := "Host lab\n    NoSuchOption yes\n"
	config := writeSSHConfig(t, home, existing, 0600)
	broken := func(q sshQuery) (string, error) {
		if q.config == config {
			return "", errors.New(config + ": line 2: Bad configuration option: nosuchoption")
		}
		return unconfiguredSSH(q.alias), nil
	}
	var stderr bytes.Buffer
	_, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(""), broken, nil))
	if err == nil || !strings.Contains(err.Error(), "SSH configuration query for build.example failed") || !strings.Contains(err.Error(), "Bad configuration option") {
		t.Fatalf("error=%v", err)
	}
	if got := readConfig(t, config); got != existing {
		t.Fatalf("config=%q", got)
	}
}

// TestSSHAliasCreatesNothingWhenCancelledOrQueryFails proves a cancelled alias question or a failing ssh -G query leaves a home
// without .ssh unchanged, at the configureSSHAlias seam with scripted stdin and query results.
func TestSSHAliasCreatesNothingWhenCancelledOrQueryFails(t *testing.T) {
	failing := func(sshQuery) (string, error) { return "", errors.New("ssh: unavailable") }
	for _, test := range []struct {
		name, target string
		resolve      func(sshQuery) (string, error)
	}{
		{"cancelled question", "192.0.2.10", unconfigured},
		{"failing query", "build.example", failing},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			var stderr bytes.Buffer
			if _, err := configureSSHAlias(test.target, "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(""), test.resolve, nil)); err == nil {
				t.Fatal("configureSSHAlias succeeded")
			}
			if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
				t.Fatalf("home entries=%v error=%v", entries, err)
			}
		})
	}
}

// TestSSHAliasRefusesHeldConfigurationLock proves a configuration lock held by another workflow refuses the alias without a query or write
// and leaves that lock in place, at the configureSSHAlias seam over a temporary home.
func TestSSHAliasRefusesHeldConfigurationLock(t *testing.T) {
	home := t.TempDir()
	existing := "# hand-written\n"
	config := writeSSHConfig(t, home, existing, 0600)
	lock := filepath.Join(home, ".ssh", ".config.lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		t.Fatal(err)
	}
	var queries []sshQuery
	var stderr bytes.Buffer
	_, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(""), unconfigured, &queries))
	if err == nil || !strings.Contains(err.Error(), "in progress") || len(queries) != 0 {
		t.Fatalf("error=%v queries=%v", err, queries)
	}
	if got := readConfig(t, config); got != existing {
		t.Fatalf("config=%q", got)
	}
	if info, err := os.Stat(lock); err != nil || !info.IsDir() {
		t.Fatalf("held lock removed: %v", err)
	}
}

// lockObservingReader fails the test when an alias answer is read while the configuration lock exists.
type lockObservingReader struct {
	t     *testing.T
	lock  string
	input io.Reader
}

// Read checks that no configuration lock is held before passing through one read of the scripted answers.
func (r lockObservingReader) Read(data []byte) (int, error) {
	if _, err := os.Stat(r.lock); err == nil {
		r.t.Fatal("alias question read while the configuration lock was held")
	}
	return r.input.Read(data)
}

// TestSSHAliasLocksQueriesButNotQuestions proves every conflict query and the write run under the configuration lock while alias questions,
// including the one after a conflict, wait outside it, and the lock is released afterwards, at the configureSSHAlias seam.
func TestSSHAliasLocksQueriesButNotQuestions(t *testing.T) {
	home := t.TempDir()
	config := writeSSHConfig(t, home, "Host lab\n    HostName 203.0.113.9\n", 0600)
	lock := filepath.Join(home, ".ssh", ".config.lock")
	resolve := func(q sshQuery) (string, error) {
		if _, err := os.Stat(lock); err != nil {
			t.Fatalf("query %v ran without the configuration lock", q)
		}
		if q.config == config && q.alias == "lab" {
			return strings.Replace(unconfiguredSSH(q.alias), "hostname lab\n", "hostname 203.0.113.9\n", 1), nil
		}
		return unconfiguredSSH(q.alias), nil
	}
	stdin := lockObservingReader{t: t, lock: lock, input: strings.NewReader("lab\nlab2\n")}
	var queries []sshQuery
	var stderr bytes.Buffer
	if alias, err := configureSSHAlias("192.0.2.10", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, stdin, resolve, &queries)); err != nil || alias != "lab2" {
		t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr.String())
	}
	if len(queries) != 4 {
		t.Fatalf("queries=%v", queries)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("configuration lock left behind: %v", err)
	}
}

// TestSSHAliasRefusesNonAgentBeforeHomeAccess proves container root is refused before home data is read or ssh -G starts, at the configureSSHAlias seam's verifyAgent identity check.
func TestSSHAliasRefusesNonAgentBeforeHomeAccess(t *testing.T) {
	var queries []sshQuery
	effects := aliasEffects(t, t.TempDir(), strings.NewReader(""), unconfigured, &queries)
	effects.UIDs = func() (int, int, int) { return 0, 0, 0 }
	effects.GIDs = func() (int, int, int) { return 0, 0, 0 }
	effects.HomeDirectory = func() (string, error) { t.Fatal("home data read before identity check"); return "", nil }
	var stderr bytes.Buffer
	if _, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, effects); err == nil || len(queries) != 0 {
		t.Fatalf("error=%v queries=%v", err, queries)
	}
	if !strings.Contains(stderr.String(), "UID and GID 1000") {
		t.Fatalf("diagnostics=%q", stderr.String())
	}
}

// TestSSHAliasRefusesInvalidAliasNames proves alias answers with spaces, patterns, a leading dash, or no text are refused and asked again before any query or write, at the configureSSHAlias seam with scripted standard input.
func TestSSHAliasRefusesInvalidAliasNames(t *testing.T) {
	home := t.TempDir()
	var queries []sshQuery
	var stderr bytes.Buffer
	answers := "lab ProxyCommand touch pwned\n*\n-oProxyCommand=x\n\nlab\n"
	alias, err := configureSSHAlias("192.0.2.10", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(answers), unconfigured, &queries))
	if err != nil || alias != "lab" {
		t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr.String())
	}
	if strings.Count(stderr.String(), "SSH alias name for 192.0.2.10") != 5 || strings.Count(stderr.String(), "invalid SSH alias name") != 4 {
		t.Fatalf("diagnostics=%q", stderr.String())
	}
	for _, q := range queries {
		if q.alias != "lab" {
			t.Fatalf("invalid alias reached ssh -G: %v", q)
		}
	}
	want := aliasEntry("lab", "192.0.2.10", "alice", filepath.Join(home, ".ssh", "id_ed25519_remote_test"))
	if got := readConfig(t, filepath.Join(home, ".ssh", "config")); got != want {
		t.Fatalf("config=%q", got)
	}
}

// TestSSHAliasEscapesTokensAndBackslashes proves a zoned IPv6 HostName and a username with a backslash are written with OpenSSH's %% and \\ escapes, at the configureSSHAlias seam's written entry.
func TestSSHAliasEscapesTokensAndBackslashes(t *testing.T) {
	home := t.TempDir()
	var stderr bytes.Buffer
	if _, err := configureSSHAlias("fe80::1%eth0", `dom\alice`, "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader("linklocal\n"), unconfigured, nil)); err != nil {
		t.Fatalf("error=%v diagnostics=%q", err, stderr.String())
	}
	want := aliasEntry("linklocal", "fe80::1%%eth0", `dom\\alice`, filepath.Join(home, ".ssh", "id_ed25519_remote_test"))
	if got := readConfig(t, filepath.Join(home, ".ssh", "config")); got != want {
		t.Fatalf("config=%q\nwant %q", got, want)
	}
}

// TestSSHAliasResolvesThroughRealOpenSSH proves, where the local OpenSSH client is installed, that the written entry beside a benign Host * option
// resolves hostname, user, identityfile, identitiesonly, and the default agent known_hosts through the real ssh -G -F FILE, without a network connection.
func TestSSHAliasResolvesThroughRealOpenSSH(t *testing.T) {
	if _, err := os.Stat("/usr/bin/ssh"); err != nil {
		t.Skip("the local OpenSSH client is not installed")
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	config := writeSSHConfig(t, home, "# kept by hand\nHost *\n    ServerAliveInterval 30\n", 0600)
	effects := aliasEffects(t, home, strings.NewReader("linklocal\n"), nil, nil)
	effects.Start = func(p Process) error {
		if p.Program != "/usr/bin/ssh" || p.Identity != agentIdentity || len(p.Args) == 0 || p.Args[0] != "-G" {
			t.Fatalf("unexpected process: %+v", p)
		}
		c := exec.Command(p.Program, p.Args...)
		c.Stdin, c.Stdout, c.Stderr = p.Stdin, p.Stdout, p.Stderr
		c.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "LC_ALL=C"}
		return c.Run()
	}
	var stderr bytes.Buffer
	if _, err := configureSSHAlias("fe80::1%eth0", `dom\alice`, "id_ed25519_remote_test", &stderr, effects); err != nil {
		t.Fatalf("error=%v diagnostics=%q", err, stderr.String())
	}
	output, err := exec.Command("/usr/bin/ssh", "-G", "-F", config, "linklocal").Output()
	if err != nil {
		t.Fatalf("ssh -G linklocal: %v", err)
	}
	resolved := map[string][]string{}
	for _, line := range strings.Split(string(output), "\n") {
		if key, value, ok := strings.Cut(line, " "); ok {
			resolved[key] = append(resolved[key], value)
		}
	}
	for key, want := range map[string]string{
		"hostname":       "fe80::1%eth0",
		"user":           `dom\alice`,
		"identityfile":   filepath.Join(home, ".ssh", "id_ed25519_remote_test"),
		"identitiesonly": "yes",
	} {
		if got := resolved[key]; len(got) != 1 || got[0] != want {
			t.Errorf("%s=%q want %q", key, got, want)
		}
	}
	// The #109 decision keeps confirmed host keys in the agent's usual known_hosts, so the entry adds no UserKnownHostsFile.
	knownHosts := resolved["userknownhostsfile"]
	if len(knownHosts) != 1 || strings.Fields(knownHosts[0])[0] != filepath.Join(account.HomeDir, ".ssh", "known_hosts") {
		t.Errorf("userknownhostsfile=%q want the passwd home's .ssh/known_hosts first", knownHosts)
	}
}
