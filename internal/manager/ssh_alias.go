package manager

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SSH configuration queries use the trusted OpenSSH client and bound what they keep of its output.
const (
	sshProgram    = "/usr/bin/ssh"
	sshQueryLimit = 64 << 10
)

// sshAliasName admits one literal Host pattern: no spaces, wildcards, negation, tokens, or leading dash.
var sshAliasName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)

// configureSSHAlias chooses the alias for a normalized outbound SSH target and appends its host entry,
// with the remote username and the dedicated key keyName in ~/.ssh, to the agent's ~/.ssh/config.
// A machine name is its own alias; an address asks for one on stderr and reads the answer from
// Effects.Stdin. An alias that the configuration already resolves, through its own entry or through
// Host * and other matching options, is reported and asked again; end of input cancels. It verifies
// the agent identity before reading home data, queries ssh -G as agent, and writes nothing unless
// it returns the chosen alias. Callers run it only after the connection test has succeeded (ADR-0011).
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
	if err := root.Mkdir(".ssh", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("cannot create SSH directory")
	}
	info, err := root.Lstat(".ssh")
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !sshFileOwned(info) {
		return "", fmt.Errorf("SSH directory must be owned by agent with mode 0700; SSH configuration unchanged")
	}
	relative := filepath.Join(".ssh", "config")
	config := "none"
	if info, err := root.Lstat(relative); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || !sshFileOwned(info) {
			return "", fmt.Errorf("SSH configuration must be a regular file owned by agent and not writable by others; SSH configuration unchanged")
		}
		config = filepath.Join(home, relative)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("cannot inspect SSH configuration")
	}
	alias, err := chooseSSHAlias(target, config, stderr, effects)
	if err != nil {
		return "", err
	}
	entry := fmt.Sprintf("Host %s\n    HostName %s\n    User %s\n    IdentityFile %s\n    IdentitiesOnly yes\n", alias, hostName, userName, identity)
	if err := appendSSHConfig(root, relative, entry); err != nil {
		return "", err
	}
	return alias, nil
}

// chooseSSHAlias returns the first valid alias the configuration file does not resolve yet,
// starting from a machine-name target and asking again after each invalid or conflicting answer.
func chooseSSHAlias(target, config string, stderr io.Writer, effects Effects) (string, error) {
	stdin := effects.Stdin
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	input := bufio.NewReader(stdin)
	// An address has no readable name, so the user picks the alias.
	alias := target
	if _, err := netip.ParseAddr(target); err == nil {
		alias = ""
	}
	for {
		if alias == "" {
			answer, err := askSSHAlias(input, target, stderr)
			if err != nil {
				return "", err
			}
			alias = answer
		}
		if !sshAliasName.MatchString(alias) {
			fmt.Fprintln(stderr, "invalid SSH alias name; use letters, digits, dots, dashes, or underscores, starting with a letter or digit")
			alias = ""
			continue
		}
		conflict, err := sshAliasConflict(config, alias, effects)
		if err != nil {
			return "", err
		}
		if !conflict {
			return alias, nil
		}
		fmt.Fprintf(stderr, "SSH alias %s is already configured in the agent's SSH configuration; nothing written, choose another alias name\n", alias)
		alias = ""
	}
}

// askSSHAlias asks for an alias name and reads one answer line; end of input cancels without writing.
func askSSHAlias(input *bufio.Reader, target string, stderr io.Writer) (string, error) {
	fmt.Fprintf(stderr, "SSH alias name for %s: ", target)
	line, err := input.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("no SSH alias name given; SSH configuration unchanged")
	}
	return strings.TrimSpace(line), nil
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

// appendSSHConfig appends the entry after the existing bytes, left unchanged, creating the file with mode 0600.
// A blank line separates the entry from earlier content, which gains a final newline first when it lacks one.
func appendSSHConfig(root *os.Root, relative, entry string) error {
	file, err := root.OpenFile(relative, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("cannot open SSH configuration")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("cannot inspect SSH configuration")
	}
	if size := info.Size(); size > 0 {
		last := make([]byte, 1)
		if _, err := file.ReadAt(last, size-1); err != nil {
			return fmt.Errorf("cannot read SSH configuration")
		}
		separator := "\n"
		if last[0] != '\n' {
			separator = "\n\n"
		}
		entry = separator + entry
	}
	if _, err := file.WriteString(entry); err != nil {
		return fmt.Errorf("cannot write SSH configuration; check its end for a partial host entry")
	}
	return file.Close()
}

// sshAliasConflict reports whether the configuration file, or "none" when it is absent, resolves the
// alias in any option differently from an unconfigured client. Unlike the host-side SSH setup, which
// compares only HostName, every option counts: a hand-written entry or Host * option would merge into
// or override the appended entry, because OpenSSH keeps the first value it reads.
func sshAliasConflict(config, alias string, effects Effects) (bool, error) {
	configured, err := sshResolve(config, alias, effects)
	if err != nil {
		return false, err
	}
	defaults, err := sshResolve("none", alias, effects)
	if err != nil {
		return false, err
	}
	return configured != defaults, nil
}

// sshQueryBuffer keeps at most sshQueryLimit bytes of query output and drains the rest.
type sshQueryBuffer struct {
	data     []byte
	overflow bool
}

// Write captures query output up to the limit and records whether any was dropped.
func (b *sshQueryBuffer) Write(data []byte) (int, error) {
	n := len(data)
	if len(data) > sshQueryLimit-len(b.data) {
		data = data[:sshQueryLimit-len(b.data)]
		b.overflow = true
	}
	b.data = append(b.data, data...)
	return n, nil
}

// sshResolve returns the effective client configuration ssh -G reports for the alias, queried as agent.
func sshResolve(config, alias string, effects Effects) (string, error) {
	var stdout, diagnostic sshQueryBuffer
	err := effects.Start(Process{Program: sshProgram, Args: []string{"-G", "-F", config, alias}, Identity: agentIdentity, EnvironmentPolicy: SSHKeyEnvironment, Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &diagnostic})
	if err != nil {
		return "", fmt.Errorf("SSH configuration query for %s failed: %s", alias, strings.TrimSpace(string(diagnostic.data)))
	}
	if stdout.overflow {
		return "", fmt.Errorf("SSH configuration query for %s returned too much output", alias)
	}
	return string(stdout.data), nil
}
