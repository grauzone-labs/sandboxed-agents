package manager

import (
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
	// sshNoConfig is the ssh -F argument with which OpenSSH reads no configuration file at all.
	sshNoConfig = "none"
	// sshConfigLock serializes changes to the agent's SSH configuration across concurrent workflows.
	sshConfigLock = ".ssh/.config.lock"
)

// sshAliasName admits one literal Host pattern: no spaces, wildcards, negation, tokens, or leading dash.
var sshAliasName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)

// configureSSHAlias chooses the alias for a normalized outbound SSH target and adds its host entry,
// with the remote username and the dedicated key keyName in ~/.ssh, to the agent's ~/.ssh/config.
// A machine name is its own alias; an address asks for one on stderr and reads the answer line from
// Effects.Stdin without consuming later input. An alias for which the configuration already resolves
// hostname, user, identityfile, or identitiesonly, through its own entry or Host *, is reported and
// asked again; end of input cancels. It verifies the agent identity before reading home data and
// queries ssh -G as agent. It writes no host entry and leaves existing configuration content unchanged
// unless it returns the chosen alias. A missing .ssh directory is created, mode 0700, only after an alias
// is chosen and its ssh -G query succeeds, so a cancelled question or a failing query creates nothing.
// A held configuration lock refuses the change. Callers run it only after the connection test has
// succeeded (ADR-0011).
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
	hostName, hostErr := sshConfigValue(target, true)
	userName, userErr := sshConfigValue(user, false)
	identity, identityErr := sshConfigValue(filepath.Join(home, ".ssh", keyName), true)
	if hostErr != nil || userErr != nil || identityErr != nil {
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
		if _, err := inspectSSHConfig(root, home); err != nil {
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
		entry := fmt.Sprintf("Host %s\n    HostName %s\n    User %s\n    IdentityFile %s\n    IdentitiesOnly yes\n", alias, hostName, userName, identity)
		written, err := writeSSHAlias(root, home, alias, entry, effects)
		if err != nil {
			return "", err
		}
		if written {
			return alias, nil
		}
		fmt.Fprintf(stderr, "SSH alias %s is already configured in the agent's SSH configuration; nothing written, choose another alias name\n", alias)
		alias = ""
	}
}

// writeSSHAlias adds the entry unless the configuration already resolves the alias, and reports whether it wrote.
// Without a .ssh directory it first checks that the client answers, so a failing query creates nothing; it then
// creates the directory and holds the configuration lock across the conflict query and the write.
func writeSSHAlias(root *os.Root, home, alias, entry string, effects Effects) (bool, error) {
	if _, err := root.Lstat(".ssh"); errors.Is(err, os.ErrNotExist) {
		if _, err := sshResolve(sshNoConfig, alias, effects); err != nil {
			return false, err
		}
	}
	if err := prepareSSHDirectory(root); err != nil {
		return false, err
	}
	if err := root.Mkdir(sshConfigLock, 0700); err != nil {
		return false, fmt.Errorf("another SSH configuration change is in progress, or its lock %s was left behind; retry after it finishes or remove that lock; SSH configuration unchanged", filepath.Join(home, sshConfigLock))
	}
	defer root.Remove(sshConfigLock)
	config, err := inspectSSHConfig(root, home)
	if err != nil {
		return false, err
	}
	conflict, err := sshAliasConflict(config, alias, effects)
	if err != nil || conflict {
		return false, err
	}
	return true, replaceSSHConfig(root, entry)
}

// inspectSSHConfig returns the configuration file path for ssh -G -F, or sshNoConfig when the file is absent,
// and refuses a file that is not regular, is not owned by agent, or is writable by group or others, as OpenSSH does.
func inspectSSHConfig(root *os.Root, home string) (string, error) {
	relative := filepath.Join(".ssh", "config")
	info, err := root.Lstat(relative)
	if errors.Is(err, os.ErrNotExist) {
		return sshNoConfig, nil
	}
	if err != nil {
		return "", fmt.Errorf("cannot inspect SSH configuration")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || !sshFileOwned(info) {
		return "", fmt.Errorf("SSH configuration must be a regular file owned by agent and not writable by others; SSH configuration unchanged")
	}
	return filepath.Join(home, relative), nil
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

// replaceSSHConfig writes the previous configuration bytes, unchanged, followed by the entry to a new file in .ssh and
// renames it over the configuration, so readers see the old or the new file and a failed write leaves the old one intact.
// The new file keeps the previous mode, or 0600 when there was none; earlier content without a final newline gains one,
// and a blank line separates the entry from it. The temporary file is removed when any step fails.
func replaceSSHConfig(root *os.Root, entry string) error {
	relative := filepath.Join(".ssh", "config")
	var previous []byte
	mode := os.FileMode(0600)
	if info, err := root.Lstat(relative); err == nil {
		if previous, err = root.ReadFile(relative); err != nil {
			return fmt.Errorf("cannot read SSH configuration; SSH configuration unchanged")
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot inspect SSH configuration")
	}
	if len(previous) > 0 {
		if previous[len(previous)-1] != '\n' {
			entry = "\n" + entry
		}
		entry = "\n" + entry
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("cannot name a temporary SSH configuration; SSH configuration unchanged")
	}
	temporary := filepath.Join(".ssh", fmt.Sprintf(".config.%x.tmp", suffix))
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("cannot create a temporary SSH configuration; SSH configuration unchanged")
	}
	_, writeErr := file.Write(append(previous, entry...))
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
		writeErr = root.Rename(temporary, relative)
	}
	if writeErr != nil {
		root.Remove(temporary)
		return fmt.Errorf("cannot write SSH configuration; SSH configuration unchanged")
	}
	return nil
}

// sshAliasOptions are the effective options the host entry sets; an alias conflicts when the configuration already
// resolves any of them differently from an unconfigured client, because OpenSSH keeps the first value it reads.
var sshAliasOptions = []string{"hostname", "user", "identityfile", "identitiesonly"}

// sshAliasConflict reports whether the configuration file, or sshNoConfig when it is absent, resolves hostname, user,
// identityfile, or identitiesonly for the alias differently from an unconfigured client. A hand-written entry or a
// Host * value for one of these options conflicts; other options, such as ServerAliveInterval, do not.
func sshAliasConflict(config, alias string, effects Effects) (bool, error) {
	configured, err := sshResolve(config, alias, effects)
	if err != nil {
		return false, err
	}
	defaults, err := sshResolve(sshNoConfig, alias, effects)
	if err != nil {
		return false, err
	}
	return sshAliasValues(configured) != sshAliasValues(defaults), nil
}

// sshAliasValues keeps the ssh -G lines of the options the host entry sets, in output order, and drops every other line.
func sshAliasValues(output string) string {
	var kept strings.Builder
	for _, line := range strings.Split(output, "\n") {
		key, _, _ := strings.Cut(line, " ")
		if slices.Contains(sshAliasOptions, key) {
			kept.WriteString(line + "\n")
		}
	}
	return kept.String()
}

// sshResolve returns the effective client configuration ssh -G reports for the alias, queried as agent with the fixed SSH
// environment. config is a configuration file path or sshNoConfig, OpenSSH's "none", for the unconfigured client.
func sshResolve(config, alias string, effects Effects) (string, error) {
	stdout, diagnostic := sshOutputBuffer{limit: sshQueryLimit}, sshOutputBuffer{limit: sshQueryLimit}
	err := effects.Start(Process{Program: sshProgram, Args: []string{"-G", "-F", config, alias}, Identity: agentIdentity, EnvironmentPolicy: SSHEnvironment, Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &diagnostic})
	if err != nil {
		return "", fmt.Errorf("SSH configuration query for %s failed: %s", alias, strings.TrimSpace(string(diagnostic.data)))
	}
	if stdout.overflow {
		return "", fmt.Errorf("SSH configuration query for %s returned too much output", alias)
	}
	return string(stdout.data), nil
}
