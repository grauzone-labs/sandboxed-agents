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

// sshQuery is one ssh -G request: whether it asks for stanza diagnostics, with -vvv for a claim query on the configuration
// file or -vv for a candidate query, its configuration file argument, the alias, and the candidate configuration it
// received on standard input.
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

// probeResolution is what ssh -G reports for a placeholder probe when no earlier rule sets the entry's options: each option resolves to
// the probe's own placeholder, as the local OpenSSH client does for a configuration followed by the trailing placeholder Host * block.
func probeResolution(alias, input string) (string, bool) {
	for name, identitiesOnly := range map[string]string{"a": "yes", "b": "no"} {
		placeholder := sshProbePlaceholder + name
		if strings.Contains(input, "HostName "+placeholder+".invalid\n") {
			return "host " + alias + "\nuser " + placeholder + "\nhostname " + placeholder + ".invalid\nport 22\nidentitiesonly " + identitiesOnly +
				"\nidentityfile /nonexistent/" + placeholder + "\nuserknownhostsfile /nonexistent/" + placeholder + "\n", true
		}
	}
	return "", false
}

// acceptEntry answers verbose queries with no Host or Match block applied, placeholder probes for the alias and the control alias with
// no option set earlier, and candidate queries with the entry's values in effect.
func acceptEntry(hostName, user, home string) func(sshQuery) sshAnswer {
	return func(q sshQuery) sshAnswer {
		var answer sshAnswer
		if q.verbose {
			answer.stderr = "OpenSSH_9.2p1\ndebug1: Reading configuration data " + q.config + "\n"
		}
		if probe, ok := probeResolution(q.alias, q.input); ok {
			answer.stdout = probe
		} else if !q.verbose {
			answer.stdout = entryResolution(q.alias, hostName, user, home)
		}
		return answer
	}
}

// withOption returns ssh -G output in which the option of changed, an ssh -G line such as "user bob", resolves to that line alone.
func withOption(output, changed string) string {
	key, _, ok := strings.Cut(changed, " ")
	if !ok {
		return output
	}
	var lines []string
	for _, line := range strings.SplitAfter(output, "\n") {
		if !strings.HasPrefix(line, key+" ") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "") + changed + "\n"
}

// claimAlias answers like accept, except that verbose queries for alias also report the given OpenSSH diagnostic lines and, when changed
// names an ssh -G line such as "user bob", the placeholder probes for alias, but not for the control alias, resolve that option to it,
// as a block specific to the alias that explicitly sets the option makes OpenSSH do.
func claimAlias(accept func(sshQuery) sshAnswer, alias, diagnostics, changed string) func(sshQuery) sshAnswer {
	return func(q sshQuery) sshAnswer {
		answer := accept(q)
		if q.alias != alias {
			return answer
		}
		if q.verbose {
			answer.stderr += diagnostics
		}
		if _, probe := probeResolution(q.alias, q.input); probe {
			answer.stdout = withOption(answer.stdout, changed)
		}
		return answer
	}
}

// aliasEffects supplies a verified agent with a temporary home and stdin, and answers ssh at the process seam from a script. It checks
// that every query runs the trusted client as agent with the fixed SSH environment and is a -vvv claim query on the configuration file
// or a query, with or without -vv, that reads a candidate configuration, the one about to be written or a placeholder probe, from /dev/stdin.
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
			if p.Program != sshProgram || p.Identity != agentIdentity || p.EnvironmentPolicy != SSHEnvironment {
				t.Fatalf("unexpected process: %+v", p)
			}
			var q sshQuery
			switch {
			case len(p.Args) == 5 && p.Args[0] == "-vvv" && p.Args[1] == "-G" && p.Args[2] == "-F" && filepath.IsAbs(p.Args[3]) && p.Args[3] != sshCandidateConfig:
				q = sshQuery{verbose: true, config: p.Args[3], alias: p.Args[4]}
			case len(p.Args) == 4 && p.Args[0] == "-G" && p.Args[1] == "-F" && p.Args[2] == sshCandidateConfig,
				len(p.Args) == 5 && p.Args[0] == "-vv" && p.Args[1] == "-G" && p.Args[2] == "-F" && p.Args[3] == sshCandidateConfig:
				input, err := io.ReadAll(p.Stdin)
				if err != nil {
					t.Fatal(err)
				}
				q = sshQuery{verbose: p.Args[0] == "-vv", config: sshCandidateConfig, alias: p.Args[len(p.Args)-1], input: string(input)}
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

// claimedText is the neutral part of the message configureSSHAlias prints when a specific SSH configuration block claims an alias.
const claimedText = "is already claimed by a specific SSH configuration block that sets "

// claimMessage is the whole line configureSSHAlias prints when a specific SSH configuration block claims alias by setting the listed keywords.
func claimMessage(alias, keywords string) string {
	return "SSH alias " + alias + " " + claimedText + keywords + "; nothing written, choose another alias name\n"
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

// configureTestAlias runs configureSSHAlias for target with the remote user alice and the dedicated test key over aliasEffects, answering
// alias questions from answers and ssh -G queries from resolve and recording queries when queries is not nil, and returns the alias,
// what it printed on stderr, and its error.
func configureTestAlias(t *testing.T, home, target, answers string, resolve func(sshQuery) sshAnswer, queries *[]sshQuery) (string, string, error) {
	t.Helper()
	var stderr bytes.Buffer
	alias, err := configureSSHAlias(target, "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, strings.NewReader(answers), resolve, queries))
	return alias, stderr.String(), err
}

// requireAppendedEntry fails the test unless the agent's ~/.ssh/config in home holds exactly existing, a blank line, and the entry for alias
// with hostName and the remote user alice.
func requireAppendedEntry(t *testing.T, home, existing, alias, hostName string) {
	t.Helper()
	if got, want := readConfig(t, filepath.Join(home, ".ssh", "config")), existing+"\n"+aliasEntry(alias, hostName, "alice", home); got != want {
		t.Fatalf("config=%q\nwant %q", got, want)
	}
}

// TestSSHAliasMachineNameUsesTargetWithoutQuestion proves a machine-name target becomes the alias without a question, its one ssh -G
// check reads exactly the entry about to be written, and the entry is written, at the configureSSHAlias seam with scripted ssh -G.
func TestSSHAliasMachineNameUsesTargetWithoutQuestion(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	var queries []sshQuery
	alias, stderr, err := configureTestAlias(t, home, "build.example", "", acceptEntry("build.example", "alice", home), &queries)
	if err != nil || alias != "build.example" {
		t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr)
	}
	if strings.Contains(stderr, "alias name") {
		t.Fatalf("machine name asked for an alias: %q", stderr)
	}
	want := aliasEntry("build.example", "build.example", "alice", home)
	if len(queries) != 1 || queries[0].alias != "build.example" || queries[0].input != want {
		t.Fatalf("queries=%v", queries)
	}
	if got := readConfig(t, filepath.Join(home, ".ssh", "config")); got != want {
		t.Fatalf("config=%q\nwant %q", got, want)
	}
}

// TestSSHAliasCreatesMissingConfigurationAsAgent proves a missing .ssh directory and config file are created owned by the agent with modes
// 0700 and 0600, at the configureSSHAlias seam over a temporary home.
func TestSSHAliasCreatesMissingConfigurationAsAgent(t *testing.T) {
	home := t.TempDir()
	if _, stderr, err := configureTestAlias(t, home, "build.example", "", acceptEntry("build.example", "alice", home), nil); err != nil {
		t.Fatalf("error=%v diagnostics=%q", err, stderr)
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

// TestSSHAliasAddressTargetAsksForAlias proves IPv4 and IPv6 targets ask the user for an alias name and write an entry for that alias with
// the address as HostName, at the configureSSHAlias seam with scripted standard input.
func TestSSHAliasAddressTargetAsksForAlias(t *testing.T) {
	for target, alias := range map[string]string{"192.0.2.10": "lab", "2001:db8::1": "lab-v6"} {
		t.Run(target, func(t *testing.T) {
			home := t.TempDir()
			got, stderr, err := configureTestAlias(t, home, target, alias+"\n", acceptEntry(target, "alice", home), nil)
			if err != nil || got != alias || strings.Count(stderr, "SSH alias name for "+target) != 1 {
				t.Fatalf("alias=%q error=%v diagnostics=%q", got, err, stderr)
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
	if _, stderr, err := configureTestAlias(t, home, "build.example", "", acceptEntry("build.example", "alice", home), &queries); err != nil {
		t.Fatalf("error=%v diagnostics=%q", err, stderr)
	}
	want := existing + "\n\n" + aliasEntry("build.example", "build.example", "alice", home)
	if got := readConfig(t, config); got != want {
		t.Fatalf("config=%q\nwant %q", got, want)
	}
	if len(queries) != 2 || !queries[0].verbose || queries[0].config != config || queries[1].input != want {
		t.Fatalf("queries=%v", queries)
	}
}

// TestSSHAliasConflictWritesNothingAndAsksAgain proves an alias that a Host pattern other than *, a Host glob, or a Match host or originalhost
// block claims by explicitly setting hostname, user, identityfile, identitiesonly, or userknownhostsfile, even to the intended value or the
// client default, writes nothing, is reported with that option, and asks for another alias name, at the configureSSHAlias seam with
// scripted ssh -vvv -G diagnostics that name a block absent from the hand-written file, so the placeholder probes name the option.
func TestSSHAliasConflictWritesNothingAndAsksAgain(t *testing.T) {
	for _, test := range []struct {
		name, target, answers, claimed, diagnostics, changed, alias string
	}{
		{"hand-written address alias", "192.0.2.10", "lab\nlab2\n", "lab", "debug1: CONFIG line 3: Applying options for lab\n", "hostname 203.0.113.9", "lab2"},
		{"machine name stanza with the intended user", "build.example", "build2\n", "build.example", "debug1: CONFIG line 3: Applying options for build.example\n", "user alice", "build2"},
		{"Host glob", "192.0.2.10", "lab-1\nlab2\n", "lab-1", "debug1: CONFIG line 3: Applying options for lab-*\n", "identitiesonly yes", "lab2"},
		{"Match host block", "192.0.2.10", "lab\nlab2\n", "lab", "debug2: checking match for 'host lab' host lab originally lab\ndebug2: match found\n", "user bob", "lab2"},
		{"Match originalhost block", "192.0.2.10", "lab\nlab2\n", "lab", "debug2: checking match for 'originalhost lab' host lab originally lab\ndebug2: match found\n", "userknownhostsfile /elsewhere", "lab2"},
		{"Host ALIAS with the default IdentitiesOnly", "192.0.2.10", "lab\nlab2\n", "lab", "debug1: CONFIG line 1: Applying options for lab\n", "identitiesonly no", "lab2"},
		{"rerun every alias claimed", "build.example", "", "build.example", "debug1: CONFIG line 3: Applying options for build.example\n", "identityfile ~/.ssh/id_ed25519_remote_test", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			existing := "# hand-written\n"
			config := writeSSHConfig(t, home, existing, 0600)
			resolve := claimAlias(acceptEntry(test.target, "alice", home), test.claimed, strings.ReplaceAll(test.diagnostics, "CONFIG", config), test.changed)
			alias, stderr, err := configureTestAlias(t, home, test.target, test.answers, resolve, nil)
			keyword := sshEntryOptions[slices.IndexFunc(sshEntryOptions, func(option sshEntryOption) bool { return option.key == strings.Fields(test.changed)[0] })].keyword
			if !strings.Contains(stderr, claimMessage(test.claimed, keyword)) || !strings.Contains(stderr, "SSH alias name for "+test.target) {
				t.Fatalf("conflict not reported or no new question: %q", stderr)
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

// TestSSHAliasAcceptsSpecificStanzaWithUnrelatedOptions proves a narrower Host glob or a Match host block that matches the alias but sets none
// of hostname, user, identityfile, identitiesonly, or userknownhostsfile, such as one setting only Port, does not claim the alias, at the
// configureSSHAlias seam with scripted ssh -vvv -G diagnostics that locate the block in the configuration file.
func TestSSHAliasAcceptsSpecificStanzaWithUnrelatedOptions(t *testing.T) {
	for name, test := range map[string]struct{ existing, diagnostics string }{
		"Port-only Host glob":  {"Host build.*\n    Port 2222\n", "debug1: CONFIG line 1: Applying options for build.*\n"},
		"Port-only Match host": {"Match host build.example\n    Port 2222\n", "debug2: checking match for 'host build.example' host build.example originally build.example\ndebug3: CONFIG line 1: matched 'host \"build.example\"'\ndebug2: match found\n"},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			existing := test.existing
			config := writeSSHConfig(t, home, existing, 0600)
			resolve := claimAlias(acceptEntry("build.example", "alice", home), "build.example", strings.ReplaceAll(test.diagnostics, "CONFIG", config), "port 2222")
			alias, stderr, err := configureTestAlias(t, home, "build.example", "", resolve, nil)
			if err != nil || alias != "build.example" || strings.Contains(stderr, claimedText) {
				t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr)
			}
			requireAppendedEntry(t, home, existing, "build.example", "build.example")
		})
	}
}

// TestSSHAliasClaimsLiteralHostBlockWithAnyOption proves a hand-written Host block whose matching pattern is the alias itself claims the
// alias when it sets any option, even only Port, so nothing is written and another alias name is asked for, also for a machine name and
// when the block's source cannot be read, while an empty Host ALIAS block claims nothing, at the configureSSHAlias seam with scripted
// ssh -vvv -G diagnostics that locate the block.
func TestSSHAliasClaimsLiteralHostBlockWithAnyOption(t *testing.T) {
	for _, test := range []struct {
		name, target, existing, location, answers, claimed, keywords, alias string
	}{
		{"Port-only Host ALIAS", "192.0.2.10", "Host lab\n    Port 2222\n    ServerAliveInterval 30\n", "CONFIG line 1", "lab\nlab2\n", "lab", "Port, ServerAliveInterval", "lab2"},
		{"Port-only machine name", "build.example", "# hand-written\nHost build.example\n    Port 2222\n", "CONFIG line 2", "build2\n", "build.example", "Port", "build2"},
		{"literal pattern beside a glob", "192.0.2.10", "Host lab-* lab\n    Port 2222\n", "CONFIG line 1", "lab\nlab2\n", "lab", "Port", "lab2"},
		{"unreadable Host ALIAS source", "192.0.2.10", "# hand-written\n", "/outside/home/config line 4", "lab\nlab2\n", "lab", "options at /outside/home/config line 4", "lab2"},
		{"empty Host ALIAS", "192.0.2.10", "Host lab\n", "CONFIG line 1", "lab\n", "", "", "lab"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			config := writeSSHConfig(t, home, test.existing, 0600)
			pattern := test.claimed
			if pattern == "" {
				pattern = test.alias
			}
			diagnostics := "debug1: " + strings.ReplaceAll(test.location, "CONFIG", config) + ": Applying options for " + pattern + "\n"
			resolve := claimAlias(acceptEntry(test.target, "alice", home), pattern, diagnostics, "")
			alias, stderr, err := configureTestAlias(t, home, test.target, test.answers, resolve, nil)
			if err != nil || alias != test.alias {
				t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr)
			}
			if test.claimed == "" {
				if strings.Contains(stderr, claimedText) {
					t.Fatalf("empty block claimed the alias: %q", stderr)
				}
			} else if !strings.Contains(stderr, claimMessage(test.claimed, test.keywords)) || !strings.Contains(stderr, "SSH alias name for "+test.target) {
				t.Fatalf("conflict not reported or no new question: %q", stderr)
			}
			requireAppendedEntry(t, home, test.existing, test.alias, test.target)
		})
	}
}

// TestSSHAliasLimitsBlockSourceReads proves a Host lab block source of exactly sshSourceLimit bytes, inside the agent's home or outside it,
// is read so its User claims lab by name, while a source one byte longer is not inspected, so the literal block still claims lab by its file
// and line and lab2 is chosen, at the configureSSHAlias seam with scripted ssh -vvv -G diagnostics that locate the block in that source.
func TestSSHAliasLimitsBlockSourceReads(t *testing.T) {
	block := "Host lab\n    User carol\n"
	for _, test := range []struct {
		name    string
		outside bool
		size    int
	}{
		{"home source at the limit", false, sshSourceLimit},
		{"home source over the limit", false, sshSourceLimit + 1},
		{"outside source at the limit", true, sshSourceLimit},
		{"outside source over the limit", true, sshSourceLimit + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			existing := "# hand-written\n"
			writeSSHConfig(t, home, existing, 0600)
			source := filepath.Join(home, ".ssh", "included")
			if test.outside {
				source = filepath.Join(t.TempDir(), "included")
			}
			padding := strings.Repeat("#", test.size-len(block)-1) + "\n"
			if err := os.WriteFile(source, []byte(block+padding), 0600); err != nil {
				t.Fatal(err)
			}
			resolve := claimAlias(acceptEntry("192.0.2.10", "alice", home), "lab", "debug1: "+source+" line 1: Applying options for lab\n", "")
			alias, stderr, err := configureTestAlias(t, home, "192.0.2.10", "lab\nlab2\n", resolve, nil)
			keywords := "User"
			if test.size > sshSourceLimit {
				keywords = "options at " + source + " line 1"
			}
			if err != nil || alias != "lab2" || !strings.Contains(stderr, claimMessage("lab", keywords)) {
				t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr)
			}
			requireAppendedEntry(t, home, existing, "lab2", "192.0.2.10")
		})
	}
}

// TestSSHAliasTellsGlobalSettingsFromSpecificClaims proves a readable Port-only Host *.lan block beside Host * IdentitiesOnly yes claims nothing
// without placeholder probes, so the machine name is used without a question; that for a block whose source cannot be read in the agent's home,
// an entry option the placeholder probes resolve to the same earlier value for the alias and the control alias is global; and that the same
// probes claim the alias when the control alias also matches a specific block, at the configureSSHAlias seam with scripted ssh -vvv -G
// diagnostics and placeholder probes.
func TestSSHAliasTellsGlobalSettingsFromSpecificClaims(t *testing.T) {
	for _, test := range []struct {
		name, location, controlDiagnostics string
		probes, claimed                    bool
	}{
		{"readable Port-only glob beside a global setting", "CONFIG line 3", "", false, false},
		{"unreadable glob with a global setting", "/outside/home/config line 1", "", true, false},
		{"unreadable glob with the control alias matching a specific block", "/outside/home/config line 1", fmt.Sprintf("debug1: %s line 3: Applying options for %s*\n", sshCandidateConfig, strings.TrimSuffix(sshControlAlias, "control")), true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			existing := "Host *\n    IdentitiesOnly yes\nHost *.lan\n    Port 2222\n"
			config := writeSSHConfig(t, home, existing, 0600)
			accept := acceptEntry("build.lan", "alice", home)
			resolve := func(q sshQuery) sshAnswer {
				answer := accept(q)
				if q.verbose && q.alias == "build.lan" {
					answer.stderr += "debug1: " + strings.ReplaceAll(test.location, "CONFIG", config) + ": Applying options for *.lan\n"
				}
				if q.verbose && q.alias == sshControlAlias {
					answer.stderr += test.controlDiagnostics
				}
				if _, probe := probeResolution(q.alias, q.input); probe {
					answer.stdout = withOption(answer.stdout, "identitiesonly yes")
				}
				return answer
			}
			var queries []sshQuery
			alias, stderr, err := configureTestAlias(t, home, "build.lan", "", resolve, &queries)
			if probed := slices.ContainsFunc(queries, func(q sshQuery) bool { _, probe := probeResolution(q.alias, q.input); return probe }); probed != test.probes {
				t.Fatalf("probes=%v queries=%v", probed, queries)
			}
			if test.claimed {
				if err == nil || !strings.Contains(stderr, claimMessage("build.lan", "IdentitiesOnly")) || readConfig(t, config) != existing {
					t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr)
				}
				return
			}
			if err != nil || alias != "build.lan" || strings.Contains(stderr, "SSH alias name for") {
				t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr)
			}
			requireAppendedEntry(t, home, existing, "build.lan", "build.lan")
		})
	}
}

// TestSSHAliasReplacesConfigurationAtomically proves the entry reaches ~/.ssh/config by replacing the file in one rename: a reader of the
// previous file still sees exactly the previous bytes, the new file keeps the previous mode, and no temporary file remains, at the
// configureSSHAlias seam.
func TestSSHAliasReplacesConfigurationAtomically(t *testing.T) {
	home := t.TempDir()
	existing := "# kept by hand\nHost other\n    User bob\n"
	config := writeSSHConfig(t, home, existing, 0644)
	previous, err := os.Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close()
	if _, stderr, err := configureTestAlias(t, home, "build.example", "", acceptEntry("build.example", "alice", home), nil); err != nil {
		t.Fatalf("error=%v diagnostics=%q", err, stderr)
	}
	if old, err := io.ReadAll(previous); err != nil || string(old) != existing {
		t.Fatalf("previous file changed in place: %q %v", old, err)
	}
	requireAppendedEntry(t, home, existing, "build.example", "build.example")
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
	writeSSHConfig(t, home, existing, 0600)
	accept := acceptEntry("build.example", "alice", home)
	resolve := claimAlias(func(q sshQuery) sshAnswer {
		answer := accept(q)
		answer.stdout = strings.NewReplacer("port 22\n", "port 2222\n", "serveraliveinterval 0\n", "serveraliveinterval 30\n").Replace(answer.stdout)
		return answer
	}, "build.example", "debug2: checking match for 'all' host build.example originally build.example\ndebug2: match found\n", "")
	alias, stderr, err := configureTestAlias(t, home, "build.example", "", resolve, nil)
	if err != nil || alias != "build.example" || strings.Contains(stderr, claimedText) {
		t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr)
	}
	requireAppendedEntry(t, home, existing, "build.example", "build.example")
}

// TestSSHAliasAcceptsGlobalValuesEqualToEntry proves Host * values for User, IdentitiesOnly, and UserKnownHostsFile that already equal the entry's
// values do not block the alias, at the configureSSHAlias seam with scripted ssh -G of the resulting configuration.
func TestSSHAliasAcceptsGlobalValuesEqualToEntry(t *testing.T) {
	home := t.TempDir()
	existing := "Host *\n    User alice\n    IdentitiesOnly yes\n    UserKnownHostsFile " + filepath.Join(home, ".ssh", "known_hosts") + "\n"
	writeSSHConfig(t, home, existing, 0600)
	alias, stderr, err := configureTestAlias(t, home, "192.0.2.10", "lab\n", acceptEntry("192.0.2.10", "alice", home), nil)
	if err != nil || alias != "lab" || strings.Contains(stderr, claimedText) {
		t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr)
	}
	requireAppendedEntry(t, home, existing, "lab", "192.0.2.10")
}

// TestSSHAliasRefusesGlobalValuesContradictingEntry proves an earlier global User or IdentitiesOnly that OpenSSH would keep instead of the entry's
// value, or a Host * or pre-block IdentityFile that OpenSSH would offer beside the dedicated key, fails once with an error naming the option, the
// alias, and why OpenSSH resolves it so, without claiming the setting applies to every alias, asks no further question, and writes nothing, at
// the configureSSHAlias seam with scripted ssh -G.
func TestSSHAliasRefusesGlobalValuesContradictingEntry(t *testing.T) {
	for _, test := range []struct{ name, existing, line, replacement, reason string }{
		{"User", "Host *\n    User bob\n", "user alice\n", "user bob\n", "keeps the first value it reads"},
		{"IdentitiesOnly", "Host *\n    IdentitiesOnly no\n", "identitiesonly yes\n", "identitiesonly no\n", "keeps the first value it reads"},
		{"Host star IdentityFile", "Host *\n    IdentityFile ~/.ssh/id_rsa\n", "identityfile ", "identityfile ~/.ssh/id_rsa\nidentityfile ", "offers every IdentityFile that applies"},
		{"pre-block IdentityFile", "IdentityFile ~/.ssh/id_rsa\nHost other\n    User bob\n", "identityfile ", "identityfile ~/.ssh/id_rsa\nidentityfile ", "offers every IdentityFile that applies"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			existing := test.existing
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
			// The error is about this alias's resolution; an earlier rule need not apply to every alias.
			if err == nil || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "for alias lab") || !strings.Contains(err.Error(), test.reason) || !strings.Contains(err.Error(), "change that setting") || strings.Contains(err.Error(), "every alias") {
				t.Fatalf("error=%v", err)
			}
			if key == "identityfile" && strings.Contains(err.Error(), "keeps the first value") {
				t.Fatalf("IdentityFile error claims OpenSSH keeps the first value: %v", err)
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
	resolve := claimAlias(acceptEntry("192.0.2.10", "alice", home), "lab", "debug1: "+config+" line 1: Applying options for lab\n", "hostname 203.0.113.9")
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
	_, _, err := configureTestAlias(t, home, "build.example", "", broken, nil)
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
			if _, _, err := configureTestAlias(t, home, test.target, "", test.resolve, nil); err == nil {
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
	lock := filepath.Join(home, sshConfigLock)
	if err := os.Mkdir(lock, 0700); err != nil {
		t.Fatal(err)
	}
	var queries []sshQuery
	_, _, err := configureTestAlias(t, home, "build.example", "", acceptEntry("build.example", "alice", home), &queries)
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
	lock := filepath.Join(home, sshConfigLock)
	claimed := claimAlias(acceptEntry("192.0.2.10", "alice", home), "lab", "debug1: "+config+" line 1: Applying options for lab\n", "hostname 203.0.113.9")
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
	// lab: the claim query, whose Host lab block at line 1 sets HostName; lab2: the claim query and the candidate check.
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
			if _, stderr, err := configureTestAlias(t, home, "build.example", "", acceptEntry("build.example", "alice", home), &queries); err != nil {
				t.Fatalf("error=%v diagnostics=%q", err, stderr)
			}
			if len(queries) != 1 {
				t.Fatalf("queries=%v", queries)
			}
		})
	}
}

// TestSSHAliasRefusesNonAgentBeforeHomeAccess proves container root is refused before home data is read or ssh -G starts, at the
// configureSSHAlias seam's verifyAgent identity check.
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

// TestSSHAliasRefusesInvalidAliasNames proves alias answers with spaces, patterns, a leading dash, or no text are refused and asked again
// before any query or write, at the configureSSHAlias seam with scripted standard input.
func TestSSHAliasRefusesInvalidAliasNames(t *testing.T) {
	home := t.TempDir()
	var queries []sshQuery
	answers := "lab ProxyCommand touch pwned\n*\n-oProxyCommand=x\n\nlab\n"
	alias, stderr, err := configureTestAlias(t, home, "192.0.2.10", answers, acceptEntry("192.0.2.10", "alice", home), &queries)
	if err != nil || alias != "lab" {
		t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr)
	}
	if strings.Count(stderr, "SSH alias name for 192.0.2.10") != 5 || strings.Count(stderr, "invalid SSH alias name") != 4 {
		t.Fatalf("diagnostics=%q", stderr)
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

// TestSSHAliasRefusesInvalidMachineNameWithoutQuestion proves a machine-name target that is not a valid alias, such as one starting with an
// underscore or longer than 253 characters, fails once with an error naming it, asks no question, reads no input, runs no ssh -G query,
// and creates nothing, at the configureSSHAlias seam with scripted standard input.
func TestSSHAliasRefusesInvalidMachineNameWithoutQuestion(t *testing.T) {
	for name, target := range map[string]string{"leading underscore": "_build.example", "254 characters without root dot": strings.Repeat("a", 254)} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			stdin := strings.NewReader("lab\n")
			var queries []sshQuery
			var stderr bytes.Buffer
			alias, err := configureSSHAlias(target, "alice", "id_ed25519_remote_test", &stderr, aliasEffects(t, home, stdin, acceptEntry(target, "alice", home), &queries))
			if err == nil || alias != "" || !strings.Contains(err.Error(), "machine name "+target+" is not a valid SSH alias") || !strings.Contains(err.Error(), "SSH configuration unchanged") {
				t.Fatalf("alias=%q error=%v", alias, err)
			}
			if strings.Contains(stderr.String(), "SSH alias name for") || strings.Contains(stderr.String(), "invalid SSH alias name") || len(queries) != 0 {
				t.Fatalf("diagnostics=%q queries=%v", stderr.String(), queries)
			}
			if rest, _ := io.ReadAll(stdin); string(rest) != "lab\n" {
				t.Fatalf("remaining input=%q", rest)
			}
			if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
				t.Fatalf("home entries=%v error=%v", entries, err)
			}
		})
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

// realSSHEnvironment returns the fixed environment that sshEnvironment gives SSH processes, with HOME pointing at the
// temporary home instead of the agent's.
func realSSHEnvironment(home string) []string {
	environment := sshEnvironment()
	for i, variable := range environment {
		if strings.HasPrefix(variable, "HOME=") {
			environment[i] = "HOME=" + home
		}
	}
	return environment
}

// realSSHEffects runs ssh -G through the local OpenSSH client at the process seam, skipping the test where none is installed;
// queries only resolve configuration and make no network connection.
func realSSHEffects(t *testing.T, home string, stdin io.Reader) Effects {
	t.Helper()
	if _, err := os.Stat(sshProgram); err != nil {
		t.Skip("the local OpenSSH client is not installed")
	}
	effects := aliasEffects(t, home, stdin, nil, nil)
	effects.Start = func(p Process) error {
		if p.Program != sshProgram || p.Identity != agentIdentity || slices.Index(p.Args, "-G") < 0 {
			t.Fatalf("unexpected process: %+v", p)
		}
		c := exec.Command(p.Program, p.Args...)
		c.Stdin, c.Stdout, c.Stderr = p.Stdin, p.Stdout, p.Stderr
		c.Env = realSSHEnvironment(home)
		return c.Run()
	}
	return effects
}

// TestSSHAliasResolvesThroughRealOpenSSH proves with the local OpenSSH client, where installed, that the entry beside benign or matching
// Host * options resolves hostname, user, identityfile, identitiesonly, and the agent home's known_hosts through ssh -G -F FILE; that a
// contradicting Host * or Host * lab value, both global because their positive * matches every host, or a Host * or pre-block IdentityFile,
// which OpenSSH offers beside the dedicated key, fails once without another question; that an existing entry, Host ALIAS, Host glob, or
// Match host or originalhost block, with its criterion keyword in any letter case such as Match Host or Match OriginalHost and also after a
// quoted Match exec command, explicitly setting one of those options, even to the intended value or to the value ssh -G -F none reports,
// asks for another alias with the neutral claim message, also when an earlier Host * block masks that setting, and that a negated Host *
// !foo block setting one of them claims every alias it matches, so only foo is accepted; that a literal Host ALIAS block setting only Port
// and ServerAliveInterval also asks for another alias, while an empty Host ALIAS block does not; that a Host glob or Match host block
// setting only Port, or Match all, does not claim the alias, also beside a Host * block that sets one of those options to the intended
// value, so a machine name is used directly without a question; that universal Host ?*, quoted Host "*", and Match host * blocks setting
// the intended value claim nothing; and that a valid 254-character machine name with its trailing DNS root dot is used directly as its own
// alias without a question. The ssh -G -F FILE query that proves those options, like every query configureSSHAlias runs, passes -F with the
// agent's own configuration, so a system-wide Host * setting in the image's OpenSSH configuration is outside both the check and this test.
// It exercises the configureSSHAlias seam with Effects.Start running the real /usr/bin/ssh -G as the process seam; the Match exec fixture
// runs only the shell builtin true.
func TestSSHAliasResolvesThroughRealOpenSSH(t *testing.T) {
	// A valid 254-character machine name: four labels of at most 63 characters and the trailing DNS root dot.
	longName := strings.Repeat("a", 63) + "." + strings.Repeat("a", 63) + "." + strings.Repeat("a", 63) + "." + strings.Repeat("b", 61) + "."
	for _, test := range []struct {
		name, target, user, existing, answers, alias, failure string
		claimed                                               bool
	}{
		{"benign Host star with escapes", "fe80::1%eth0", `dom\alice`, "# kept by hand\nHost *\n    ServerAliveInterval 30\n", "linklocal\n", "linklocal", "", false},
		{"Host star equal to the entry", "192.0.2.10", "alice", "Host *\n    User alice\n    IdentitiesOnly yes\n    UserKnownHostsFile HOME/.ssh/known_hosts\n", "lab\n", "lab", "", false},
		{"Host star other user", "192.0.2.10", "alice", "Host *\n    User bob\n", "lab\nlab2\n", "", "user", false},
		{"Host star IdentitiesOnly no", "192.0.2.10", "alice", "Host *\n    IdentitiesOnly no\n", "lab\nlab2\n", "", "identitiesonly", false},
		{"Host star IdentityFile", "192.0.2.10", "alice", "Host *\n    IdentityFile ~/.ssh/id_rsa\n", "lab\nlab2\n", "", "identityfile", false},
		{"pre-block IdentityFile", "192.0.2.10", "alice", "IdentityFile ~/.ssh/id_rsa\nHost other\n    User bob\n", "lab\nlab2\n", "", "identityfile", false},
		{"rerun with the same entry", "192.0.2.10", "alice", "ENTRY", "lab\nlab2\n", "lab2", "", true},
		{"Port-only Host ALIAS", "192.0.2.10", "alice", "Host lab\n    Port 2222\n    ServerAliveInterval 30\n", "lab\nlab2\n", "lab2", "", true},
		{"empty Host ALIAS", "192.0.2.10", "alice", "Host lab\n", "lab\n", "lab", "", false},
		{"Host ALIAS with the intended user", "192.0.2.10", "alice", "Host lab\n    User alice\n", "lab\nlab2\n", "lab2", "", true},
		{"Port-only Host glob", "192.0.2.10", "alice", "Host lab-*\n    Port 2\n", "lab-1\n", "lab-1", "", false},
		{"Host glob with IdentitiesOnly", "192.0.2.10", "alice", "Host lab-*\n    IdentitiesOnly yes\n", "lab-1\nlab2\n", "lab2", "", true},
		{"Port-only Match host", "192.0.2.10", "alice", "Match host lab\n    Port 3\n", "lab\n", "lab", "", false},
		{"Match host with HostName", "192.0.2.10", "alice", "Match host lab\n    HostName 203.0.113.9\n", "lab\nlab2\n", "lab2", "", true},
		{"Match originalhost with UserKnownHostsFile", "192.0.2.10", "alice", "Match originalhost lab\n    UserKnownHostsFile /elsewhere\n", "lab\nlab2\n", "lab2", "", true},
		{"capitalized Match Host with User", "192.0.2.10", "alice", "Match Host lab\n    User bob\n", "lab\nlab2\n", "lab2", "", true},
		{"capitalized Match OriginalHost with User", "192.0.2.10", "alice", "Match OriginalHost lab\n    User bob\n", "lab\nlab2\n", "lab2", "", true},
		{"Host star masking Host lab with another user", "192.0.2.10", "alice", "Host *\n    User alice\nHost lab\n    User carol\n", "lab\nlab2\n", "lab2", "", true},
		{"Host star masking Host lab with the intended user", "192.0.2.10", "alice", "Host *\n    User alice\nHost lab\n    User alice\n", "lab\nlab2\n", "lab2", "", true},
		{"Host star negation with the intended user", "192.0.2.10", "alice", "Host * !foo\n    User alice\n", "lab\nfoo\n", "foo", "", true},
		{"universal Host ?* with the intended user", "192.0.2.10", "alice", "Host ?*\n    User alice\n", "lab\n", "lab", "", false},
		{"universal Match host * with the intended user", "192.0.2.10", "alice", "Match host *\n    User alice\n", "lab\n", "lab", "", false},
		{"254-character trailing-dot machine name", longName, "alice", "NONE", "", longName, "", false},
		{"quoted Host star with the intended user", "192.0.2.10", "alice", "Host \"*\"\n    User alice\n", "lab\n", "lab", "", false},
		{"Match exec with a quoted command and host lab", "192.0.2.10", "alice", "Match exec \"true x\" host lab\n    User bob\n", "lab\nlab2\n", "lab2", "", true},
		{"Host ALIAS with the default IdentitiesOnly", "192.0.2.10", "alice", "Host lab\n    IdentitiesOnly no\n", "lab\nlab2\n", "lab2", "", true},
		{"Match host with the default UserKnownHostsFile", "192.0.2.10", "alice", "Match host lab\n    UserKnownHostsFile ~/.ssh/known_hosts ~/.ssh/known_hosts2\n", "lab\nlab2\n", "lab2", "", true},
		{"Host ALIAS with a default IdentityFile", "192.0.2.10", "alice", "Host lab\n    IdentityFile ~/.ssh/id_rsa\n", "lab\nlab2\n", "lab2", "", true},
		{"Match all claims nothing", "192.0.2.10", "alice", "Match all\n    Compression yes\n", "lab\n", "lab", "", false},
		{"Port-only Host glob beside Host star IdentitiesOnly", "build.lan", "alice", "Host *\n    IdentitiesOnly yes\nHost *.lan\n    Port 2222\n", "", "build.lan", "", false},
		{"Host star lab equal to the entry", "192.0.2.10", "alice", "Host * lab\n    IdentitiesOnly yes\n", "lab\n", "lab", "", false},
		{"Host star lab other user", "192.0.2.10", "alice", "Host * lab\n    User bob\n", "lab\nlab2\n", "", "user", false},
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
			if test.claimed != strings.Contains(stderr.String(), claimedText) {
				t.Fatalf("claimed=%v diagnostics=%q", test.claimed, stderr.String())
			}
			query := exec.Command(sshProgram, "-G", "-F", config, alias)
			query.Env = realSSHEnvironment(home)
			output, err := query.Output()
			if err != nil {
				t.Fatalf("ssh -G %s: %v", alias, err)
			}
			resolved := sshResolvedOptions(string(output))
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

// TestSSHAliasReadsIncludedStanzasThroughRealOpenSSH proves with the local OpenSSH client, where installed, that a literal Host lab block
// from an included file inside the agent's home claims lab when it sets User or only Port, while a Port-only Host la? glob there does not;
// that a Host lab block included from outside the home claims lab, also when an earlier Host * User alice masks its User carol; and that
// a Host lab block whose body includes its options claims lab by its Include, while a Host la? glob whose body includes User claims lab
// by that User, so lab2 is chosen after each claim with the claiming keywords reported. It exercises the configureSSHAlias seam with
// Effects.Start running the real /usr/bin/ssh -G as the process seam.
func TestSSHAliasReadsIncludedStanzasThroughRealOpenSSH(t *testing.T) {
	for _, test := range []struct {
		name, config, included, answers, alias, keywords string
		outside                                          bool
	}{
		{"home include setting User", "Include INCLUDED\n", "Host lab\n    User carol\n", "lab\nlab2\n", "lab2", "User", false},
		{"home include setting only Port", "Include INCLUDED\n", "Host lab\n    Port 2222\n", "lab\nlab2\n", "lab2", "Port", false},
		{"home include with a Port-only glob", "Include INCLUDED\n", "Host la?\n    Port 2222\n", "lab\n", "lab", "", false},
		{"include outside the home setting User", "Include INCLUDED\n", "Host lab\n    User carol\n", "lab\nlab2\n", "lab2", "User", true},
		{"Include inside a Host lab block", "Host lab\n    Include INCLUDED\n", "User carol\n", "lab\nlab2\n", "lab2", "Include", false},
		{"Include inside a Host lab glob", "Host la?\n    Include INCLUDED\n", "User carol\n", "lab\nlab2\n", "lab2", "User", false},
		{"Host star masking an outside-home Host lab", "Host *\n    User alice\nInclude INCLUDED\n", "Host lab\n    User carol\n", "lab\nlab2\n", "lab2", "User", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			included := filepath.Join(home, ".ssh", "included")
			if test.outside {
				included = filepath.Join(t.TempDir(), "included")
			}
			if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(included, []byte(test.included), 0600); err != nil {
				t.Fatal(err)
			}
			existing := strings.ReplaceAll(test.config, "INCLUDED", included)
			writeSSHConfig(t, home, existing, 0600)
			var stderr bytes.Buffer
			alias, err := configureSSHAlias("192.0.2.10", "alice", "id_ed25519_remote_test", &stderr, realSSHEffects(t, home, strings.NewReader(test.answers)))
			if err != nil || alias != test.alias || (test.keywords == "") == strings.Contains(stderr.String(), claimedText) || (test.keywords != "" && !strings.Contains(stderr.String(), claimMessage("lab", test.keywords))) {
				t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr.String())
			}
			requireAppendedEntry(t, home, existing, test.alias, "192.0.2.10")
		})
	}
}

// TestSSHAliasWritesMachineAliasDespiteLargeDiagnostics proves with the local OpenSSH client, where installed, that a configuration of 450
// nonmatching Match host never-N.invalid blocks, whose ssh -vvv -G diagnostics for the machine name exceed 64 KiB, still lets the machine
// name build.example become its own alias without a question and receive its entry after the existing bytes. It exercises the
// configureSSHAlias seam with Effects.Start running the real /usr/bin/ssh -G as the process seam.
func TestSSHAliasWritesMachineAliasDespiteLargeDiagnostics(t *testing.T) {
	home := t.TempDir()
	effects := realSSHEffects(t, home, strings.NewReader(""))
	var existing strings.Builder
	for i := range 450 {
		fmt.Fprintf(&existing, "Match host never-%d.invalid\n    Port 1\n", i)
	}
	config := writeSSHConfig(t, home, existing.String(), 0600)
	var diagnostics bytes.Buffer
	query := exec.Command(sshProgram, "-vvv", "-G", "-F", config, "build.example")
	query.Stdout, query.Stderr = io.Discard, &diagnostics
	query.Env = realSSHEnvironment(home)
	if err := query.Run(); err != nil || diagnostics.Len() <= sshQueryLimit {
		t.Fatalf("precondition: diagnostics=%d bytes error=%v", diagnostics.Len(), err)
	}
	var stderr bytes.Buffer
	alias, err := configureSSHAlias("build.example", "alice", "id_ed25519_remote_test", &stderr, effects)
	if err != nil || alias != "build.example" || strings.Contains(stderr.String(), "SSH alias name for") {
		t.Fatalf("alias=%q error=%v diagnostics=%q", alias, err, stderr.String())
	}
	if got := readConfig(t, config); got != existing.String()+"\n"+aliasEntry("build.example", "build.example", "alice", home) {
		t.Fatalf("config ends %q", got[max(0, len(got)-200):])
	}
}
