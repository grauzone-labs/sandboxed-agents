package manager

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// SSH alias changes query the trusted OpenSSH client, bound what they keep of its output and of each answer, and serialize on a lock directory.
const (
	sshProgram    = "/usr/bin/ssh"
	sshQueryLimit = 64 << 10
	// sshAnswerLimit exceeds any valid alias, so a longer answer is refused as invalid.
	sshAnswerLimit = 1024
	// sshCandidateConfig makes ssh -G read the configuration it would see after the write from the query's standard input.
	sshCandidateConfig = "/dev/stdin"
	// sshConfigLock serializes changes to the agent's SSH configuration across concurrent workflows.
	sshConfigLock = ".ssh/.config.lock"
)

// sshAliasName admits one literal Host pattern: no spaces, wildcards, negation, tokens, or leading dash.
var sshAliasName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)

// sshOption is one effective ssh -G option the host entry sets, with the value OpenSSH must report for the alias.
type sshOption struct{ key, value string }

// configureSSHAlias chooses the alias for a normalized outbound SSH target and adds its host entry, with the remote
// username, the dedicated key keyName in ~/.ssh, IdentitiesOnly, and the agent's ~/.ssh/known_hosts, to ~/.ssh/config.
// A machine name is its own alias; an address asks for one on stderr and reads the answer line from Effects.Stdin
// without consuming later input. An alias that a Host pattern other than * or a Match host or originalhost block
// already claims, by changing one of the entry's options, is reported and asked again; end of input cancels. When the
// candidate configuration would resolve one of the entry's values differently for the alias, because an earlier
// matching option such as one under Host * takes precedence, it fails without asking again and names that option;
// whether the earlier setting also matches other aliases depends on its own rule. It verifies the agent identity
// before reading home data and queries ssh -G as agent. It writes no host entry
// and leaves existing configuration content unchanged unless it returns the chosen alias. A missing .ssh directory is
// created, mode 0700, only after an alias is chosen and its ssh -G check succeeds, so a cancelled question or a failing
// query creates nothing. A held configuration lock refuses the change. Callers run it only after the connection test
// has succeeded (ADR-0011).
func configureSSHAlias(target, user, keyName string, stderr io.Writer, effects Effects) (string, error) {
	if !verifyAgent(effects, stderr) {
		return "", fmt.Errorf("SSH alias requires the agent identity; SSH configuration unchanged")
	}
	if effects.HomeDirectory == nil {
		return "", fmt.Errorf("SSH home data is unavailable")
	}
	home, err := effects.HomeDirectory()
	if err != nil || !filepath.IsAbs(home) {
		return "", fmt.Errorf("SSH home data is unavailable")
	}
	identityPath := filepath.Join(home, ".ssh", keyName)
	// #109 keeps confirmed host keys in the agent's usual known_hosts; the entry names it so the alias never depends on another default.
	knownHostsPath := filepath.Join(home, ".ssh", "known_hosts")
	hostName, hostErr := sshConfigValue(target, true)
	userName, userErr := sshConfigValue(user, false)
	identity, identityErr := sshConfigValue(identityPath, true)
	knownHosts, knownHostsErr := sshConfigValue(knownHostsPath, true)
	if hostErr != nil || userErr != nil || identityErr != nil || knownHostsErr != nil {
		return "", fmt.Errorf("SSH host entry values cannot be written safely; SSH configuration unchanged")
	}
	want := []sshOption{{"hostname", target}, {"user", user}, {"identityfile", identityPath}, {"identitiesonly", "yes"}, {"userknownhostsfile", knownHostsPath}}
	root, err := os.OpenRoot(home)
	if err != nil {
		return "", fmt.Errorf("cannot open SSH home data")
	}
	defer root.Close()
	// Refuse unusable existing files before asking anything; the write repeats the checks under the lock.
	if _, err := root.Lstat(".ssh"); err == nil {
		if err := checkSSHDirectory(root); err != nil {
			return "", err
		}
		if _, err := readSSHConfig(root); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("cannot inspect SSH directory")
	}
	stdin := effects.Stdin
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	// An address has no readable name, so the user picks the alias.
	alias := target
	if _, err := netip.ParseAddr(target); err == nil {
		alias = ""
	}
	for {
		if alias == "" {
			if alias, err = askSSHAlias(stdin, target, stderr); err != nil {
				return "", err
			}
		}
		if !sshAliasName.MatchString(alias) {
			fmt.Fprintln(stderr, "invalid SSH alias name; use letters, digits, dots, dashes, or underscores, starting with a letter or digit")
			alias = ""
			continue
		}
		entry := fmt.Sprintf("Host %s\n    HostName %s\n    User %s\n    IdentityFile %s\n    IdentitiesOnly yes\n    UserKnownHostsFile %s\n", alias, hostName, userName, identity, knownHosts)
		added, err := addSSHAlias(root, alias, entry, want, effects)
		if err != nil {
			return "", err
		}
		if added {
			return alias, nil
		}
		fmt.Fprintf(stderr, "SSH alias %s is already configured in the agent's SSH configuration; nothing written, choose another alias name\n", alias)
		alias = ""
	}
}

// addSSHAlias adds the entry to the configuration in the home that root opens unless a specific Host pattern or
// Match host block already claims the alias, and reports whether it did. It holds the configuration lock across the claim
// query, the check of the resulting configuration, and the atomic write. Without a .ssh directory it checks the entry
// alone first, so a failing query creates nothing, and it queries no more once the lock confirms there is still no
// configuration file.
func addSSHAlias(root *os.Root, alias, entry string, want []sshOption, effects Effects) (bool, error) {
	_, err := root.Lstat(".ssh")
	checked := errors.Is(err, os.ErrNotExist)
	if checked {
		if err := checkSSHEntry([]byte(entry), alias, want, effects); err != nil {
			return false, err
		}
	}
	if err := prepareSSHDirectory(root); err != nil {
		return false, err
	}
	if err := root.Mkdir(sshConfigLock, 0700); err != nil {
		return false, fmt.Errorf("another SSH configuration change is in progress, or its lock %s was left behind; retry after it finishes or remove that lock; SSH configuration unchanged", filepath.Join(root.Name(), sshConfigLock))
	}
	defer root.Remove(sshConfigLock)
	current, err := readSSHConfig(root)
	if err != nil {
		return false, err
	}
	if current.exists {
		claimed, err := sshAliasClaimed(filepath.Join(root.Name(), ".ssh", "config"), current.content, alias, effects)
		if err != nil || claimed {
			return false, err
		}
		checked = false
	}
	content := appendSSHEntry(current.content, entry)
	if !checked {
		if err := checkSSHEntry(content, alias, want, effects); err != nil {
			return false, err
		}
	}
	return true, replaceSSHConfig(root, current.mode, content)
}

// sshConfigFile is the agent's ~/.ssh/config as read under the lock: whether it exists, its bytes, and the mode a replacement keeps.
type sshConfigFile struct {
	exists  bool
	content []byte
	mode    os.FileMode
}

// readSSHConfig reads ~/.ssh/config, or reports it absent with the 0600 mode a new file receives, and refuses a file that is
// not regular, is not owned by agent, or is writable by group or others, as OpenSSH does.
func readSSHConfig(root *os.Root) (sshConfigFile, error) {
	relative := filepath.Join(".ssh", "config")
	info, err := root.Lstat(relative)
	if errors.Is(err, os.ErrNotExist) {
		return sshConfigFile{mode: 0600}, nil
	}
	if err != nil {
		return sshConfigFile{}, fmt.Errorf("cannot inspect SSH configuration")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || !sshFileOwned(info) {
		return sshConfigFile{}, fmt.Errorf("SSH configuration must be a regular file owned by agent and not writable by others; SSH configuration unchanged")
	}
	content, err := root.ReadFile(relative)
	if err != nil {
		return sshConfigFile{}, fmt.Errorf("cannot read SSH configuration; SSH configuration unchanged")
	}
	return sshConfigFile{exists: true, content: content, mode: info.Mode().Perm()}, nil
}

// appendSSHEntry returns the previous configuration bytes, unchanged, followed by the entry; earlier content without a final
// newline gains one, and a blank line separates the entry from it.
func appendSSHEntry(previous []byte, entry string) []byte {
	if len(previous) == 0 {
		return []byte(entry)
	}
	content := append([]byte{}, previous...)
	if content[len(content)-1] != '\n' {
		content = append(content, '\n')
	}
	return append(append(content, '\n'), entry...)
}

// askSSHAlias asks for an alias name and reads one answer line; end of input cancels without writing.
func askSSHAlias(stdin io.Reader, target string, stderr io.Writer) (string, error) {
	fmt.Fprintf(stderr, "SSH alias name for %s: ", target)
	line, err := readSSHAnswer(stdin)
	if err != nil {
		return "", fmt.Errorf("no SSH alias name given; SSH configuration unchanged")
	}
	return strings.TrimSpace(line), nil
}

// readSSHAnswer reads one line one byte at a time, so input after its newline stays unread for the caller.
// It keeps at most sshAnswerLimit bytes of a longer line and returns a final line without newline at end of input.
func readSSHAnswer(stdin io.Reader) (string, error) {
	var line []byte
	next := make([]byte, 1)
	for {
		n, err := stdin.Read(next)
		if n == 1 {
			if next[0] == '\n' {
				return string(line), nil
			}
			if len(line) < sshAnswerLimit {
				line = append(line, next[0])
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) > 0 {
				return string(line), nil
			}
			return "", err
		}
	}
}

// sshConfigValue escapes one ssh_config argument so OpenSSH reads it back unchanged:
// backslashes always, and percent signs where the keyword expands tokens. It refuses
// whitespace, control characters, quotes, and comment marks instead of quoting them.
func sshConfigValue(value string, tokens bool) (string, error) {
	if value == "" || strings.ContainsFunc(value, func(r rune) bool { return r <= ' ' || r == 0x7f || strings.ContainsRune("\"'#", r) }) {
		return "", fmt.Errorf("unsafe SSH configuration value")
	}
	value = strings.ReplaceAll(value, `\`, `\\`)
	if tokens {
		value = strings.ReplaceAll(value, "%", "%%")
	}
	return value, nil
}

// replaceSSHConfig writes content to a new file in .ssh with the given mode and renames it over the configuration, so readers
// see the old or the new file and a failed write leaves the old one intact. The temporary file is removed when any step fails.
func replaceSSHConfig(root *os.Root, mode os.FileMode, content []byte) error {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("cannot name a temporary SSH configuration; SSH configuration unchanged")
	}
	temporary := filepath.Join(".ssh", fmt.Sprintf(".config.%x.tmp", suffix))
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("cannot create a temporary SSH configuration; SSH configuration unchanged")
	}
	_, writeErr := file.Write(content)
	if writeErr == nil {
		writeErr = file.Chmod(mode)
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr == nil {
		writeErr = root.Rename(temporary, filepath.Join(".ssh", "config"))
	}
	if writeErr != nil {
		root.Remove(temporary)
		return fmt.Errorf("cannot write SSH configuration; SSH configuration unchanged")
	}
	return nil
}

// sshProbeKeywords names, for each ssh -G option the host entry sets, the ssh_config keyword a placeholder probe sets.
var sshProbeKeywords = []sshOption{{"hostname", "HostName"}, {"user", "User"}, {"identityfile", "IdentityFile"}, {"identitiesonly", "IdentitiesOnly"}, {"userknownhostsfile", "UserKnownHostsFile"}}

// sshAliasClaimed reports whether the configuration already claims the alias. OpenSSH's -vv diagnostics name each applied
// Host pattern and each matched Match block; a Host pattern other than *, or a Match block with a host or originalhost
// criterion, is specific to the alias. Such a block claims the alias only when an earlier rule explicitly sets one of the
// entry's options, even to the intended value or to OpenSSH's default, so a rerun finds its own entry while a block that
// sets only Port or ServerAliveInterval claims nothing. The diagnostics do not say which options a block sets, so
// sshOptionsSetEarlier asks OpenSSH. Global blocks are left to checkSSHEntry.
func sshAliasClaimed(config string, content []byte, alias string, effects Effects) (bool, error) {
	_, diagnostics, err := runSSHQuery([]string{"-vv", "-G", "-F", config, alias}, nil, alias, effects)
	if err != nil || !sshSpecificStanza(diagnostics) {
		return false, err
	}
	return sshOptionsSetEarlier(content, alias, effects)
}

// sshOptionsSetEarlier reports whether the configuration explicitly sets one of the entry's options for the alias. It has
// ssh -G resolve the configuration followed by a trailing Host * block of placeholder values, twice with opposite
// IdentitiesOnly values; because OpenSSH keeps the first value it reads, an option that does not resolve to exactly its
// placeholder in both probes was set by an earlier rule, and an earlier IdentityFile stays ahead of the placeholder.
func sshOptionsSetEarlier(content []byte, alias string, effects Effects) (bool, error) {
	for _, probe := range []struct{ name, identitiesOnly string }{{"a", "yes"}, {"b", "no"}} {
		placeholder := "sandboxed-agents-probe-" + probe.name
		values := map[string]string{"hostname": placeholder + ".invalid", "user": placeholder, "identityfile": "/nonexistent/" + placeholder, "identitiesonly": probe.identitiesOnly, "userknownhostsfile": "/nonexistent/" + placeholder}
		block := "Host *\n"
		var want []sshOption
		for _, option := range sshProbeKeywords {
			block += "    " + option.value + " " + values[option.key] + "\n"
			want = append(want, sshOption{option.key, values[option.key]})
		}
		differing, err := sshCandidateDifferences(appendSSHEntry(content, block), alias, want, effects)
		if err != nil || len(differing) > 0 {
			return len(differing) > 0, err
		}
	}
	return false, nil
}

// sshSpecificStanza reports whether ssh -vv diagnostics show an applied Host pattern other than * or a matched Match
// block whose criteria include host or originalhost, as opposed to Host *, Match all, or other global criteria.
func sshSpecificStanza(diagnostics string) bool {
	hostCriteria := false
	for _, line := range strings.Split(diagnostics, "\n") {
		if _, pattern, ok := strings.Cut(line, ": Applying options for "); ok && strings.HasPrefix(line, "debug1: ") && strings.TrimSpace(pattern) != "*" {
			return true
		}
		if rest, ok := strings.CutPrefix(line, "debug2: checking match for '"); ok {
			criteria, _, _ := strings.Cut(rest, "' host ")
			hostCriteria = slices.ContainsFunc(strings.Fields(criteria), func(word string) bool { return word == "host" || word == "originalhost" })
		}
		if strings.TrimSpace(line) == "debug2: match found" && hostCriteria {
			return true
		}
	}
	return false
}

// checkSSHEntry has ssh -G resolve the alias from the exact configuration bytes about to be written, read from the query's
// standard input, and refuses them when an earlier option in that candidate configuration that matches the alias, such as
// one under Host * or a Match block without host criteria, takes precedence because OpenSSH keeps the first value it reads,
// so the alias would resolve another value for an option the entry sets. The error names the alias and the option and asks
// for the earlier setting to be changed instead of offering another question; whether that setting also matches other
// aliases depends on its own rule.
func checkSSHEntry(content []byte, alias string, want []sshOption, effects Effects) error {
	overridden, err := sshCandidateDifferences(content, alias, want, effects)
	if err != nil {
		return err
	}
	if len(overridden) > 0 {
		return fmt.Errorf("an earlier setting in the agent's SSH configuration would still apply for alias %s, which OpenSSH would resolve with %s because it keeps the first value it reads; change that setting and retry; SSH configuration unchanged", alias, strings.Join(overridden, "; "))
	}
	return nil
}

// sshCandidateDifferences has ssh -G resolve the alias from configuration bytes read from the query's standard input and
// describes each wanted option that does not resolve to exactly its one wanted value.
func sshCandidateDifferences(content []byte, alias string, want []sshOption, effects Effects) ([]string, error) {
	output, _, err := runSSHQuery([]string{"-G", "-F", sshCandidateConfig, alias}, bytes.NewReader(content), alias, effects)
	if err != nil {
		return nil, err
	}
	resolved := map[string][]string{}
	for _, line := range strings.Split(output, "\n") {
		if key, value, ok := strings.Cut(line, " "); ok {
			resolved[key] = append(resolved[key], value)
		}
	}
	var differing []string
	for _, option := range want {
		if got := resolved[option.key]; len(got) != 1 || got[0] != option.value {
			differing = append(differing, fmt.Sprintf("%s %q instead of %q", option.key, strings.Join(got, ", "), option.value))
		}
	}
	return differing, nil
}

// runSSHQuery runs one bounded ssh query as agent with the fixed SSH environment and returns its output and diagnostics.
// A failure reports OpenSSH's own diagnostic lines without the debug lines that verbose queries add.
func runSSHQuery(args []string, stdin io.Reader, alias string, effects Effects) (string, string, error) {
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	stdout, diagnostic := sshOutputBuffer{limit: sshQueryLimit}, sshOutputBuffer{limit: sshQueryLimit}
	err := effects.Start(Process{Program: sshProgram, Args: args, Identity: agentIdentity, EnvironmentPolicy: SSHEnvironment, Stdin: stdin, Stdout: &stdout, Stderr: &diagnostic})
	if err != nil {
		var reported []string
		for _, line := range strings.Split(string(diagnostic.data), "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "debug") && !strings.HasPrefix(line, "OpenSSH_") {
				reported = append(reported, line)
			}
		}
		return "", "", fmt.Errorf("SSH configuration query for %s failed: %s", alias, strings.Join(reported, "; "))
	}
	if stdout.overflow || diagnostic.overflow {
		return "", "", fmt.Errorf("SSH configuration query for %s returned too much output", alias)
	}
	return string(stdout.data), string(diagnostic.data), nil
}
