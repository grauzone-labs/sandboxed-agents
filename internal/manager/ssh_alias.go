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
	// sshControlAlias is a host name that sshAliasName never accepts, so no chosen alias equals it; placeholder probes
	// resolve it beside the alias to tell settings every alias gets from settings specific to the alias.
	sshControlAlias = "_sandboxed-agents-control"
)

// sshAliasName admits one literal Host pattern: no spaces, wildcards, negation, tokens, or leading dash.
var sshAliasName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)

// sshEntryOption is one option the host entry sets: its ssh -G key, its ssh_config keyword, and whether the keyword
// expands % tokens, so its value needs %% escapes.
type sshEntryOption struct {
	key, keyword string
	tokens       bool
}

// sshEntryOptions lists, in entry order, every option the host entry sets; the entry lines, the values checked through
// ssh -G, and the placeholder probes are all built from it.
var sshEntryOptions = []sshEntryOption{
	{key: "hostname", keyword: "HostName", tokens: true},
	{key: "user", keyword: "User"},
	{key: "identityfile", keyword: "IdentityFile", tokens: true},
	{key: "identitiesonly", keyword: "IdentitiesOnly"},
	{key: "userknownhostsfile", keyword: "UserKnownHostsFile", tokens: true},
}

// configureSSHAlias chooses the alias for a normalized outbound SSH target and adds its host entry, with the remote
// username, the dedicated key keyName in ~/.ssh, IdentitiesOnly, and the agent's ~/.ssh/known_hosts, to ~/.ssh/config.
// A machine name is its own alias; an address asks for one on stderr and reads the answer line from Effects.Stdin
// without consuming later input. A Host pattern other than *, a Host glob, or a Match host or originalhost block that
// matches the alias claims it only by explicitly setting one of the entry's options, even to the intended value or to
// OpenSSH's default; such a claim is reported with the options it sets and the user is asked again, and end of input
// cancels. A block that matches the alias but sets only other options, such as Port, claims nothing, also beside a
// Host * setting, so a machine name is then used directly. When the candidate configuration would resolve one of the
// entry's values differently for the alias, because an earlier matching option such as one under Host * or Host * lab
// takes precedence, it fails without asking again and names that option; whether the earlier setting also matches
// other aliases depends on its own rule. It verifies the agent identity before reading home data and queries ssh -G as
// agent. It writes no host entry and leaves existing configuration content unchanged unless it returns the chosen
// alias. A missing .ssh directory is created, mode 0700, only after an alias is chosen and its ssh -G check succeeds,
// so a cancelled question or a failing query creates nothing. A held configuration lock refuses the change. Callers
// run it only after the connection test has succeeded (ADR-0011).
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
	// #109 keeps confirmed host keys in the agent's usual known_hosts; the entry names it so the alias never depends on another default.
	want := map[string]string{
		"hostname":           target,
		"user":               user,
		"identityfile":       filepath.Join(home, ".ssh", keyName),
		"identitiesonly":     "yes",
		"userknownhostsfile": filepath.Join(home, ".ssh", "known_hosts"),
	}
	lines, err := sshOptionLines(want)
	if err != nil {
		return "", fmt.Errorf("SSH host entry values cannot be written safely; SSH configuration unchanged")
	}
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
		claimed, err := addSSHAlias(root, alias, "Host "+alias+"\n"+lines, want, effects)
		if err != nil {
			return "", err
		}
		if len(claimed) == 0 {
			return alias, nil
		}
		fmt.Fprintf(stderr, "SSH alias %s is already claimed by a Host or Match host block for it that sets %s; nothing written, choose another alias name\n", alias, strings.Join(claimed, ", "))
		alias = ""
	}
}

// sshOptionLines returns the indented ssh_config lines that set each entry option to its value in values, keyed by
// ssh -G key and escaped so OpenSSH reads each value back unchanged, or an error when a value cannot be written safely.
func sshOptionLines(values map[string]string) (string, error) {
	var lines strings.Builder
	for _, option := range sshEntryOptions {
		value, err := sshConfigValue(values[option.key], option.tokens)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&lines, "    %s %s\n", option.keyword, value)
	}
	return lines.String(), nil
}

// addSSHAlias adds the entry to the configuration in the home that root opens and returns no keywords, or writes nothing
// and returns the keywords of the entry options that a Host pattern other than *, a Host glob, or a Match host or
// originalhost block explicitly sets for the alias, which claims it. It holds the configuration lock across the claim
// queries, the check of the resulting configuration, and the atomic write. Without a .ssh directory it checks the entry
// alone first, so a failing query creates nothing, and it queries no more once the lock confirms there is still no
// configuration file.
func addSSHAlias(root *os.Root, alias, entry string, want map[string]string, effects Effects) ([]string, error) {
	_, err := root.Lstat(".ssh")
	checked := errors.Is(err, os.ErrNotExist)
	if checked {
		if err := checkSSHEntry([]byte(entry), alias, want, effects); err != nil {
			return nil, err
		}
	}
	if err := prepareSSHDirectory(root); err != nil {
		return nil, err
	}
	if err := root.Mkdir(sshConfigLock, 0700); err != nil {
		return nil, fmt.Errorf("another SSH configuration change is in progress, or its lock %s was left behind; retry after it finishes or remove that lock; SSH configuration unchanged", filepath.Join(root.Name(), sshConfigLock))
	}
	defer root.Remove(sshConfigLock)
	current, err := readSSHConfig(root)
	if err != nil {
		return nil, err
	}
	if current.exists {
		claimed, err := sshAliasClaimed(filepath.Join(root.Name(), ".ssh", "config"), current.content, alias, effects)
		if err != nil || len(claimed) > 0 {
			return claimed, err
		}
		checked = false
	}
	content := appendSSHEntry(current.content, entry)
	if !checked {
		if err := checkSSHEntry(content, alias, want, effects); err != nil {
			return nil, err
		}
	}
	return nil, replaceSSHConfig(root, current.mode, content)
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

// sshAliasClaimed returns the keywords of the entry options that the configuration explicitly sets for the alias through
// a block specific to it, which claims the alias. OpenSSH's -vv diagnostics name each applied Host pattern and each
// matched Match block; a Host pattern other than *, or a Match block with a host or originalhost criterion, is specific,
// while Host *, Host * lab, whose positive * matches every host, and other Match blocks are global. Without a specific
// block nothing is claimed and global settings are left to checkSSHEntry. The diagnostics do not say which options a
// block sets, so sshOptionsSetForAlias asks OpenSSH.
func sshAliasClaimed(config string, content []byte, alias string, effects Effects) ([]string, error) {
	_, diagnostics, err := runSSHQuery([]string{"-vv", "-G", "-F", config, alias}, nil, alias, effects)
	if err != nil || !sshSpecificStanza(diagnostics) {
		return nil, err
	}
	return sshOptionsSetForAlias(content, alias, effects)
}

// sshOptionsSetForAlias returns, in entry order, the keywords of the entry options that the configuration sets for the
// alias but not equally for every alias. It has ssh -G resolve the configuration followed by a trailing Host * block of placeholder
// values, twice with opposite IdentitiesOnly values, for the alias and for sshControlAlias. Because OpenSSH keeps the
// first value it reads, an option the alias does not resolve to exactly its placeholder was set by an earlier rule; when
// the control alias, which no specific block matches, resolves the same value, a global rule set it, as with Host * beside
// a Port-only Host glob. A specific block that sets the same value as an earlier global one cannot be told apart from it
// and counts as global. When a specific block also matches the control alias, the comparison is not used.
func sshOptionsSetForAlias(content []byte, alias string, effects Effects) ([]string, error) {
	set := map[string]bool{}
	for _, probe := range []struct{ name, identitiesOnly string }{{"a", "yes"}, {"b", "no"}} {
		placeholder := "sandboxed-agents-probe-" + probe.name
		placeholders := map[string]string{
			"hostname":           placeholder + ".invalid",
			"user":               placeholder,
			"identityfile":       "/nonexistent/" + placeholder,
			"identitiesonly":     probe.identitiesOnly,
			"userknownhostsfile": "/nonexistent/" + placeholder,
		}
		lines, err := sshOptionLines(placeholders)
		if err != nil {
			return nil, err
		}
		probed := appendSSHEntry(content, "Host *\n"+lines)
		resolved, _, err := resolveSSHCandidate(probed, alias, false, effects)
		if err != nil {
			return nil, err
		}
		control, diagnostics, err := resolveSSHCandidate(probed, sshControlAlias, true, effects)
		if err != nil {
			return nil, err
		}
		controlGlobal := !sshSpecificStanza(diagnostics)
		for _, option := range sshEntryOptions {
			got := resolved[option.key]
			if slices.Equal(got, []string{placeholders[option.key]}) || (controlGlobal && slices.Equal(got, control[option.key])) {
				continue
			}
			set[option.key] = true
		}
	}
	var claimed []string
	for _, option := range sshEntryOptions {
		if set[option.key] {
			claimed = append(claimed, option.keyword)
		}
	}
	return claimed, nil
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
func checkSSHEntry(content []byte, alias string, want map[string]string, effects Effects) error {
	resolved, _, err := resolveSSHCandidate(content, alias, false, effects)
	if err != nil {
		return err
	}
	var overridden []string
	for _, option := range sshEntryOptions {
		if got := resolved[option.key]; !slices.Equal(got, []string{want[option.key]}) {
			overridden = append(overridden, fmt.Sprintf("%s %q instead of %q", option.key, strings.Join(got, ", "), want[option.key]))
		}
	}
	if len(overridden) > 0 {
		return fmt.Errorf("an earlier setting in the agent's SSH configuration would still apply for alias %s, which OpenSSH would resolve with %s because it keeps the first value it reads; change that setting and retry; SSH configuration unchanged", alias, strings.Join(overridden, "; "))
	}
	return nil
}

// resolveSSHCandidate has ssh -G resolve the host from configuration bytes read from the query's standard input and returns
// each option's values by ssh -G key, with OpenSSH's -vv diagnostics when verbose is set.
func resolveSSHCandidate(content []byte, host string, verbose bool, effects Effects) (map[string][]string, string, error) {
	args := []string{"-G", "-F", sshCandidateConfig, host}
	if verbose {
		args = append([]string{"-vv"}, args...)
	}
	output, diagnostics, err := runSSHQuery(args, bytes.NewReader(content), host, effects)
	if err != nil {
		return nil, "", err
	}
	resolved := map[string][]string{}
	for _, line := range strings.Split(output, "\n") {
		if key, value, ok := strings.Cut(line, " "); ok {
			resolved[key] = append(resolved[key], value)
		}
	}
	return resolved, diagnostics, nil
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
