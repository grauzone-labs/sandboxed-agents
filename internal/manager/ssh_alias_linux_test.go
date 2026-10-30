package manager

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// sshQuery is one ssh -G request: whether it asks for -vv stanza diagnostics, its configuration file argument, the alias,
// and the candidate configuration it received on standard input.
type sshQuery struct {
	verbose              bool
	config, alias, input string
}

// sshAnswer is a scripted ssh -G result: resolved options on standard output, diagnostics on standard error, and a failure.
type sshAnswer struct {
	stdout, stderr string
	err            error
}

// entryResolution is what ssh -G reports for an alias whose entry took effect: the target, the remote user, only the dedicated key,
// IdentitiesOnly, and the agent home's known_hosts.
func entryResolution(alias, hostName, user, home string) string {
	return "host " + alias + "\nuser " + user + "\nhostname " + hostName + "\nport 22\nidentitiesonly yes\nserveraliveinterval 0\nidentityfile " +
		filepath.Join(home, ".ssh", "id_ed25519_remote_test") + "\nuserknownhostsfile " + filepath.Join(home, ".ssh", "known_hosts") + "\n"
}

// acceptEntry answers claim queries with only the global Host * block applied and candidate queries with the entry's values in effect.
func acceptEntry(hostName, user, home string) func(sshQuery) sshAnswer {
	return func(q sshQuery) sshAnswer {
		if q.verbose {
			return sshAnswer{stderr: "OpenSSH_9.2p1\ndebug1: Reading configuration data " + q.config + "\ndebug1: " + q.config + " line 1: Applying options for *\n"}
		}
		return sshAnswer{stdout: entryResolution(q.alias, hostName, user, home)}
	}
}

// claimAlias answers like accept, except that the claim query for alias also reports the given OpenSSH diagnostic lines.
func claimAlias(accept func(sshQuery) sshAnswer, alias, diagnostics string) func(sshQuery) sshAnswer {
	return func(q sshQuery) sshAnswer {
		answer := accept(q)
		if q.verbose && q.alias == alias {
			answer.stderr += diagnostics
		}
		return answer
	}
}

// aliasEffects supplies a verified agent with a temporary home and stdin, and answers ssh at the process seam from a script. It checks
// that every query runs the trusted client as agent with the fixed SSH environment and is either a -vv claim query on the configuration
// file or a candidate query that reads the configuration about to be written from /dev/stdin.
func aliasEffects(t *testing.T, home string, stdin io.Reader, resolve func(sshQuery) sshAnswer, queries *[]sshQuery) Effects {
	t.Helper()
	return Effects{
		Stdin:         stdin,
		HomeDirectory: func() (string, error) { return home, nil },
		Identity:      func() Identity { return agentIdentity },
		Groups:        func() ([]int, error) { return nil, nil },
		UIDs:          func() (int, int, int) { return 1000, 1000, 1000 },
		GIDs:          func() (int, int, int) { return 1000, 1000, 1000 },
		Start: func(p Process) error {
			if p.Program != "/usr/bin/ssh" || p.Identity != agentIdentity || p.EnvironmentPolicy != SSHEnvironment {
				t.Fatalf("unexpected process: %+v", p)
			}
			var q sshQuery
			switch {
			case len(p.Args) == 5 && p.Args[0] == "-vv" && p.Args[1] == "-G" && p.Args[2] == "-F" && filepath.IsAbs(p.Args[3]) && p.Args[3] != "/dev/stdin":
				q = sshQuery{verbose: true, config: p.Args[3], alias: p.Args[4]}
			case len(p.Args) == 4 && p.Args[0] == "-G" && p.Args[1] == "-F" && p.Args[2] == "/dev/stdin":
				input, err := io.ReadAll(p.Stdin)
				if err != nil {
					t.Fatal(err)
				}
				q = sshQuery{config: p.Args[2], alias: p.Args[3], input: string(input)}
			default:
				t.Fatalf("unexpected ssh arguments: %q", p.Args)
			}
			if queries != nil {
				*queries = append(*queries, q)
			}
			answer := resolve(q)
			io.WriteString(p.Stderr, answer.stderr)
			if answer.err != nil {
				fmt.Fprintln(p.Stderr, answer.err)
				return errors.New("exit status 255")
			}
			_, err := io.WriteString(p.Stdout, answer.stdout)
			return err
		},
	}
}

// aliasEntry is the host entry #111 specifies, in the layout the configuration file receives: the dedicated test key and,
// as #109 decided, the agent home's known_hosts.
func aliasEntry(alias, hostName, user, home string) string {
	return "Host " + alias + "\n    HostName " + hostName + "\n    User " + user + "\n    IdentityFile " + filepath.Join(home, ".ssh", "id_ed25519_remote_test") +
		"\n    IdentitiesOnly yes\n    UserKnownHostsFile " + filepath.Join(home, ".ssh", "known_hosts") + "\n"
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

// TestSSHAliasMachineNameUsesTargetWithoutQuestion proves a machine-name target becomes the alias without a question, its one ssh -G
// check reads exactly the entry about to be written, and the entry is written, at the configureSSHAlias seam with scripted ssh -G.
func TestSSHAliasMachineNameUsesTargetWithoutQuestion(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	var queries []sshQuery
	var stderr bytes.Buffer
	alias, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(""), acceptEntry("build.example", "alice", home), &queries))
	if err != nil || alias != "build.example" {
		t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr.String())
	}
	if strings.Contains(stderr.String(), "alias name") {
		t.Fatalf("machine name asked for an alias: %q", stderr.String())
	}
	want := aliasEntry("build.example", "build.example", "alice", home)
	if len(queries) != 1 || queries[0].alias != "build.example" || queries[0].input != want {
		t.Fatalf("queries=%v", queries)
	}
	if got := readConfig(t, filepath.Join(home, ".ssh", "config")); got != want {
		t.Fatalf("config=%q\nwant %q", got, want)
	}
}

// TestSSHAliasCreatesMissingConfigurationAsAgent proves a missing .ssh directory and config file are created owned by the agent with modes 0700 and 0600, at the configureSSHAlias seam over a temporary home.
func TestSSHAliasCreatesMissingConfigurationAsAgent(t *testing.T) {
	home := t.TempDir()
	var stderr bytes.Buffer
	if _, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(""), acceptEntry("build.example", "alice", home), nil)); err != nil {
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
			got, err := configureSSHAlias(target, "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(alias+"\n"), acceptEntry(target, "alice", home), nil))
			if err != nil || got != alias || strings.Count(stderr.String(), "SSH alias name for "+target) != 1 {
				t.Fatalf("alias=%q error=%v diagnostics=%q", got, err, stderr.String())
			}
			if config := readConfig(t, filepath.Join(home, ".ssh", "config")); config != aliasEntry(alias, target, "alice", home) {
				t.Fatalf("config=%q", config)
			}
		})
	}
}

// TestSSHAliasPreservesExistingConfiguration proves existing entries, comments, and a missing final newline stay byte for byte ahead of the one
// appended entry, and that ssh -G checks exactly those bytes before they are written, at the configureSSHAlias seam over a hand-written config file.
func TestSSHAliasPreservesExistingConfiguration(t *testing.T) {
	home := t.TempDir()
	existing := "# kept by hand\nHost other\n    HostName 198.51.100.7\n    User bob\n# trailing comment without newline"
	config := writeSSHConfig(t, home, existing, 0600)
	var queries []sshQuery
	var stderr bytes.Buffer
	if _, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(""), acceptEntry("build.example", "alice", home), &queries)); err != nil {
		t.Fatalf("error=%v diagnostics=%q", err, stderr.String())
	}
	want := existing + "\n\n" + aliasEntry("build.example", "build.example", "alice", home)
	if got := readConfig(t, config); got != want {
		t.Fatalf("config=%q\nwant %q", got, want)
	}
	if len(queries) != 2 || !queries[0].verbose || queries[0].config != config || queries[1].input != want {
		t.Fatalf("queries=%v", queries)
	}
}

// TestSSHAliasConflictWritesNothingAndAsksAgain proves an alias that OpenSSH reports as claimed by a Host pattern other than *, a Host glob, or
// a Match block other than Match all, whatever options it sets, writes nothing, is reported, and asks for another alias name, at the
// configureSSHAlias seam with scripted ssh -vv -G diagnostics.
func TestSSHAliasConflictWritesNothingAndAsksAgain(t *testing.T) {
	for _, test := range []struct {
		name, target, answers, claimed, diagnostics, alias string
	}{
		{"hand-written address alias", "192.0.2.10", "lab\nlab2\n", "lab", "debug1: CONFIG line 3: Applying options for lab\n", "lab2"},
		{"Port-only machine name stanza", "build.example", "build2\n", "build.example", "debug1: CONFIG line 3: Applying options for build.example\n", "build2"},
		{"Host glob", "192.0.2.10", "lab-1\nlab2\n", "lab-1", "debug1: CONFIG line 3: Applying options for lab-*\n", "lab2"},
		{"Match host block", "192.0.2.10", "lab\nlab2\n", "lab", "debug2: checking match for 'host lab' host lab originally lab\ndebug2: match found\n", "lab2"},
		{"rerun every alias claimed", "build.example", "", "build.example", "debug1: CONFIG line 3: Applying options for build.example\n", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			existing := "# hand-written\n"
			config := writeSSHConfig(t, home, existing, 0600)
			resolve := claimAlias(acceptEntry(test.target, "alice", home), test.claimed, strings.ReplaceAll(test.diagnostics, "CONFIG", config))
			var stderr bytes.Buffer
			alias, err := configureSSHAlias(test.target, "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(test.answers), resolve, nil))
			if !strings.Contains(stderr.String(), "SSH alias "+test.claimed+" is already configured") || !strings.Contains(stderr.String(), "SSH alias name for "+test.target) {
				t.Fatalf("conflict not reported or no new question: %q", stderr.String())
			}
			got := readConfig(t, config)
			if test.alias == "" {
				if err == nil || got != existing {
					t.Fatalf("alias=%q error=%v config=%q", alias, err, got)
				}
				return
			}
			want := existing + "\n" + aliasEntry(test.alias, test.target, "alice", home)
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
	if _, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(""), acceptEntry("build.example", "alice", home), nil)); err != nil {
		t.Fatalf("error=%v diagnostics=%q", err, stderr.String())
	}
	if old, err := io.ReadAll(previous); err != nil || string(old) != existing {
		t.Fatalf("previous file changed in place: %q %v", old, err)
	}
	if got := readConfig(t, config); got != existing+"\n"+aliasEntry("build.example", "build.example", "alice", home) {
		t.Fatalf("config=%q", got)
	}
	if info, err := os.Stat(config); err != nil || info.Mode().Perm() != 0644 {
		t.Fatalf("config mode=%v error=%v", info, err)
	}
	if entries, err := os.ReadDir(filepath.Join(home, ".ssh")); err != nil || len(entries) != 1 {
		t.Fatalf("leftover files=%v error=%v", entries, err)
	}
}

// TestSSHAliasIgnoresUnrelatedEffectiveOptions proves the global Host * block and Match all, setting options outside the entry's values such as
// ServerAliveInterval or Port, do not block the alias, at the configureSSHAlias seam with scripted ssh -G.
func TestSSHAliasIgnoresUnrelatedEffectiveOptions(t *testing.T) {
	home := t.TempDir()
	existing := "Host *\n    ServerAliveInterval 30\n    Port 2222\nMatch all\n    Compression yes\n"
	config := writeSSHConfig(t, home, existing, 0600)
	accept := acceptEntry("build.example", "alice", home)
	resolve := claimAlias(func(q sshQuery) sshAnswer {
		answer := accept(q)
		answer.stdout = strings.NewReplacer("port 22\n", "port 2222\n", "serveraliveinterval 0\n", "serveraliveinterval 30\n").Replace(answer.stdout)
		return answer
	}, "build.example", "debug2: checking match for 'all' host build.example originally build.example\ndebug2: match found\n")
	var stderr bytes.Buffer
	alias, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(""), resolve, nil))
	if err != nil || alias != "build.example" || strings.Contains(stderr.String(), "already configured") {
		t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr.String())
	}
	if got := readConfig(t, config); got != existing+"\n"+aliasEntry("build.example", "build.example", "alice", home) {
		t.Fatalf("config=%q", got)
	}
}

// TestSSHAliasAcceptsGlobalValuesEqualToEntry proves Host * values for User, IdentitiesOnly, and UserKnownHostsFile that already equal the entry's
// values do not block the alias, at the configureSSHAlias seam with scripted ssh -G of the resulting configuration.
func TestSSHAliasAcceptsGlobalValuesEqualToEntry(t *testing.T) {
	home := t.TempDir()
	existing := "Host *\n    User alice\n    IdentitiesOnly yes\n    UserKnownHostsFile " + filepath.Join(home, ".ssh", "known_hosts") + "\n"
	config := writeSSHConfig(t, home, existing, 0600)
	var stderr bytes.Buffer
	alias, err := configureSSHAlias("192.0.2.10", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader("lab\n"), acceptEntry("192.0.2.10", "alice", home), nil))
	if err != nil || alias != "lab" || strings.Contains(stderr.String(), "already configured") {
		t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr.String())
	}
	if got := readConfig(t, config); got != existing+"\n"+aliasEntry("lab", "192.0.2.10", "alice", home) {
		t.Fatalf("config=%q", got)
	}
}

// TestSSHAliasRefusesGlobalValuesContradictingEntry proves an earlier global User or IdentitiesOnly that OpenSSH would keep instead of the entry's
// value fails once with an error naming the option, asks no further question, and writes nothing, at the configureSSHAlias seam with scripted ssh -G.
func TestSSHAliasRefusesGlobalValuesContradictingEntry(t *testing.T) {
	for _, test := range []struct{ name, line, replacement string }{
		{"User", "user alice\n", "user bob\n"},
		{"IdentitiesOnly", "identitiesonly yes\n", "identitiesonly no\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			existing := "Host *\n    " + strings.TrimSuffix(test.replacement, "\n") + "\n"
			config := writeSSHConfig(t, home, existing, 0600)
			accept := acceptEntry("192.0.2.10", "alice", home)
			resolve := func(q sshQuery) sshAnswer {
				answer := accept(q)
				answer.stdout = strings.Replace(answer.stdout, test.line, test.replacement, 1)
				return answer
			}
			stdin := strings.NewReader("lab\nlab2\n")
			var stderr bytes.Buffer
			_, err := configureSSHAlias("192.0.2.10", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, stdin, resolve, nil))
			key, _, _ := strings.Cut(test.replacement, " ")
			if err == nil || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "change that setting") {
				t.Fatalf("error=%v", err)
			}
			if strings.Count(stderr.String(), "SSH alias name for") != 1 || readConfig(t, config) != existing {
				t.Fatalf("diagnostics=%q config=%q", stderr.String(), readConfig(t, config))
			}
			if rest, _ := io.ReadAll(stdin); string(rest) != "lab2\n" {
				t.Fatalf("remaining input=%q", rest)
			}
		})
	}
}

// TestSSHAliasLeavesLaterInputUnread proves the alias questions consume exactly their answer lines, across a conflict and a second question,
// so a later answer for the caller stays readable on the same standard input, at the configureSSHAlias seam with scripted stdin.
func TestSSHAliasLeavesLaterInputUnread(t *testing.T) {
	home := t.TempDir()
	config := writeSSHConfig(t, home, "Host lab\n    HostName 203.0.113.9\n", 0600)
	resolve := claimAlias(acceptEntry("192.0.2.10", "alice", home), "lab", "debug1: "+config+" line 1: Applying options for lab\n")
	stdin := strings.NewReader("lab\nlab2\nlater answer\n")
	var stderr bytes.Buffer
	if alias, err := configureSSHAlias("192.0.2.10", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, stdin, resolve, nil)); err != nil || alias != "lab2" {
		t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr.String())
	}
	if rest, err := io.ReadAll(stdin); err != nil || string(rest) != "later answer\n" {
		t.Fatalf("remaining input=%q error=%v", rest, err)
	}
}

// TestSSHAliasFailingQueryWritesNothing proves a failing ssh -G query is reported with OpenSSH's diagnostic, without its debug lines, and leaves
// the file unchanged, at the configureSSHAlias seam with a scripted query failure.
func TestSSHAliasFailingQueryWritesNothing(t *testing.T) {
	home := t.TempDir()
	existing := "Host lab\n    NoSuchOption yes\n"
	config := writeSSHConfig(t, home, existing, 0600)
	broken := func(q sshQuery) sshAnswer {
		return sshAnswer{stderr: "OpenSSH_9.2p1\ndebug1: Reading configuration data " + config + "\n", err: errors.New(config + ": line 2: Bad configuration option: nosuchoption")}
	}
	var stderr bytes.Buffer
	_, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(""), broken, nil))
	if err == nil || !strings.Contains(err.Error(), "SSH configuration query for build.example failed") || !strings.Contains(err.Error(), "Bad configuration option") || strings.Contains(err.Error(), "debug1") {
		t.Fatalf("error=%v", err)
	}
	if got := readConfig(t, config); got != existing {
		t.Fatalf("config=%q", got)
	}
}

// TestSSHAliasCreatesNothingWhenCancelledOrQueryFails proves a cancelled alias question or a failing ssh -G query leaves a home
// without .ssh unchanged, at the configureSSHAlias seam with scripted stdin and query results.
func TestSSHAliasCreatesNothingWhenCancelledOrQueryFails(t *testing.T) {
	failing := func(sshQuery) sshAnswer { return sshAnswer{err: errors.New("ssh: unavailable")} }
	for _, test := range []struct {
		name, target string
		resolve      func(sshQuery) sshAnswer
	}{
		{"cancelled question", "192.0.2.10", func(sshQuery) sshAnswer { t.Fatal("query without an alias"); return sshAnswer{} }},
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
	_, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(""), acceptEntry("build.example", "alice", home), &queries))
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

// TestSSHAliasLocksQueriesButNotQuestions proves every claim query, check, and the write run under the configuration lock while alias questions,
// including the one after a conflict, wait outside it, and the lock is released afterwards, at the configureSSHAlias seam.
func TestSSHAliasLocksQueriesButNotQuestions(t *testing.T) {
	home := t.TempDir()
	config := writeSSHConfig(t, home, "Host lab\n    HostName 203.0.113.9\n", 0600)
	lock := filepath.Join(home, ".ssh", ".config.lock")
	claimed := claimAlias(acceptEntry("192.0.2.10", "alice", home), "lab", "debug1: "+config+" line 1: Applying options for lab\n")
	resolve := func(q sshQuery) sshAnswer {
		if _, err := os.Stat(lock); err != nil {
			t.Fatalf("query %v ran without the configuration lock", q)
		}
		return claimed(q)
	}
	stdin := lockObservingReader{t: t, lock: lock, input: strings.NewReader("lab\nlab2\n")}
	var queries []sshQuery
	var stderr bytes.Buffer
	if alias, err := configureSSHAlias("192.0.2.10", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, stdin, resolve, &queries)); err != nil || alias != "lab2" {
		t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr.String())
	}
	if len(queries) != 3 {
		t.Fatalf("queries=%v", queries)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("configuration lock left behind: %v", err)
	}
}

// TestSSHAliasQueriesOnceWithoutConfiguration proves a home without .ssh/config, with or without the .ssh directory, takes exactly one
// ssh -G query for the alias, at the configureSSHAlias seam with scripted queries.
func TestSSHAliasQueriesOnceWithoutConfiguration(t *testing.T) {
	for name, directory := range map[string]bool{"no .ssh directory": false, "empty .ssh directory": true} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			if directory {
				if err := os.Mkdir(filepath.Join(home, ".ssh"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			var queries []sshQuery
			var stderr bytes.Buffer
			if _, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(""), acceptEntry("build.example", "alice", home), &queries)); err != nil {
				t.Fatalf("error=%v diagnostics=%q", err, stderr.String())
			}
			if len(queries) != 1 {
				t.Fatalf("queries=%v", queries)
			}
		})
	}
}

// TestSSHAliasRefusesNonAgentBeforeHomeAccess proves container root is refused before home data is read or ssh -G starts, at the configureSSHAlias seam's verifyAgent identity check.
func TestSSHAliasRefusesNonAgentBeforeHomeAccess(t *testing.T) {
	var queries []sshQuery
	home := t.TempDir()
	effects := aliasEffects(t, home, strings.NewReader(""), acceptEntry("build.example", "alice", home), &queries)
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
	alias, err := configureSSHAlias("192.0.2.10", "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(answers), acceptEntry("192.0.2.10", "alice", home), &queries))
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
	if got := readConfig(t, filepath.Join(home, ".ssh", "config")); got != aliasEntry("lab", "192.0.2.10", "alice", home) {
		t.Fatalf("config=%q", got)
	}
}

// TestSSHAliasEscapesTokensAndBackslashes proves a zoned IPv6 HostName and a username with a backslash are written with OpenSSH's %% and \\ escapes,
// while the check expects their unescaped values, at the configureSSHAlias seam's written entry.
func TestSSHAliasEscapesTokensAndBackslashes(t *testing.T) {
	home := t.TempDir()
	var stderr bytes.Buffer
	if _, err := configureSSHAlias("fe80::1%eth0", `dom\alice`, "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader("linklocal\n"), acceptEntry("fe80::1%eth0", `dom\alice`, home), nil)); err != nil {
		t.Fatalf("error=%v diagnostics=%q", err, stderr.String())
	}
	want := aliasEntry("linklocal", "fe80::1%%eth0", `dom\\alice`, home)
	if got := readConfig(t, filepath.Join(home, ".ssh", "config")); got != want {
		t.Fatalf("config=%q\nwant %q", got, want)
	}
}

// realSSHEffects runs ssh -G through the local OpenSSH client at the process seam, skipping the test where none is installed;
// queries only resolve configuration and make no network connection.
func realSSHEffects(t *testing.T, home string, stdin io.Reader) Effects {
	t.Helper()
	if _, err := os.Stat("/usr/bin/ssh"); err != nil {
		t.Skip("the local OpenSSH client is not installed")
	}
	effects := aliasEffects(t, home, stdin, nil, nil)
	effects.Start = func(p Process) error {
		if p.Program != "/usr/bin/ssh" || p.Identity != agentIdentity || slices.Index(p.Args, "-G") < 0 {
			t.Fatalf("unexpected process: %+v", p)
		}
		c := exec.Command(p.Program, p.Args...)
		c.Stdin, c.Stdout, c.Stderr = p.Stdin, p.Stdout, p.Stderr
		c.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "LC_ALL=C"}
		return c.Run()
	}
	return effects
}

// TestSSHAliasResolvesThroughRealOpenSSH proves with the local OpenSSH client, where installed, that the entry beside benign or matching
// Host * options resolves hostname, user, identityfile, identitiesonly, and the agent home's known_hosts through ssh -G -F FILE; that a
// contradicting Host * value fails once without another question; and that an existing entry, a Host glob, or a Match host block for the
// alias asks for another one while Match all does not.
func TestSSHAliasResolvesThroughRealOpenSSH(t *testing.T) {
	for _, test := range []struct {
		name, target, user, existing, answers, alias, failure string
		claimed                                               bool
	}{
		{"benign Host star with escapes", "fe80::1%eth0", `dom\alice`, "# kept by hand\nHost *\n    ServerAliveInterval 30\n", "linklocal\n", "linklocal", "", false},
		{"Host star equal to the entry", "192.0.2.10", "alice", "Host *\n    User alice\n    IdentitiesOnly yes\n    UserKnownHostsFile HOME/.ssh/known_hosts\n", "lab\n", "lab", "", false},
		{"Host star other user", "192.0.2.10", "alice", "Host *\n    User bob\n", "lab\nlab2\n", "", "user", false},
		{"Host star IdentitiesOnly no", "192.0.2.10", "alice", "Host *\n    IdentitiesOnly no\n", "lab\nlab2\n", "", "identitiesonly", false},
		{"rerun with the same entry", "192.0.2.10", "alice", "ENTRY", "lab\nlab2\n", "lab2", "", true},
		{"Host glob claims", "192.0.2.10", "alice", "Host lab-*\n    Port 2\n", "lab-1\nlab2\n", "lab2", "", true},
		{"Match host claims", "192.0.2.10", "alice", "Match host lab\n    Port 3\n", "lab\nlab2\n", "lab2", "", true},
		{"Match all claims nothing", "192.0.2.10", "alice", "Match all\n    Compression yes\n", "lab\n", "lab", "", false},
		{"no configuration file", "build.example", "alice", "NONE", "", "build.example", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			existing := strings.ReplaceAll(test.existing, "HOME", home)
			if existing == "ENTRY" {
				existing = aliasEntry("lab", test.target, test.user, home)
			}
			config := filepath.Join(home, ".ssh", "config")
			if existing != "NONE" {
				writeSSHConfig(t, home, existing, 0600)
			}
			var stderr bytes.Buffer
			alias, err := configureSSHAlias(test.target, test.user, "id_ed25519_remote_test", &stderr, realSSHEffects(t, home, strings.NewReader(test.answers)))
			if test.failure != "" {
				if err == nil || !strings.Contains(err.Error(), test.failure) || strings.Count(stderr.String(), "SSH alias name for") != 1 || readConfig(t, config) != existing {
					t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr.String())
				}
				return
			}
			if err != nil || alias != test.alias {
				t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr.String())
			}
			if test.claimed != strings.Contains(stderr.String(), "already configured") {
				t.Fatalf("claimed=%v diagnostics=%q", test.claimed, stderr.String())
			}
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
			for key, want := range map[string]string{
				"hostname":       test.target,
				"user":           test.user,
				"identityfile":   filepath.Join(home, ".ssh", "id_ed25519_remote_test"),
				"identitiesonly": "yes",
				// #109 keeps confirmed host keys in the agent home's known_hosts, which the entry names explicitly.
				"userknownhostsfile": filepath.Join(home, ".ssh", "known_hosts"),
			} {
				if got := resolved[key]; len(got) != 1 || got[0] != want {
					t.Errorf("%s=%q want %q", key, got, want)
				}
			}
		})
	}
}
