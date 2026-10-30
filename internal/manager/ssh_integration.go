package manager

import (
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
	"github.com/grauzone-labs/sandboxed-agents/internal/sshkey"
)

// SSH key operations use a trusted executable and bound both stored and derived public material.
const (
	sshKeygenProgram = "/usr/bin/ssh-keygen"
	sshPublicLimit   = 4096
)

// errUnsafeSSHKey refuses incomplete or unsafe key pairs without replacing either file.
var errUnsafeSSHKey = errors.New("SSH key files are incomplete or unsafe; no key overwritten")

// configureSSH runs the outbound SSH workflow: it validates direct manager requests and verifies agent identity before
// accessing home data, prepares the dedicated key and prints its public key, and then asks for approval of the
// connection test. Declining the question, or cancelling it with end of input or Ctrl+C, starts no process, writes no
// host entry, keeps the key, and returns 1. After approval it runs the password connection test; any failure writes no
// host entry, leaves the SSH configuration unchanged, keeps the key, and returns 1. Only a successful test runs the
// alias host entry unit, and only its success returns 0; the report says that key-based login is not verified.
func configureSSH(args []string, stdout, stderr io.Writer, effects Effects) int {
	if _, err := catalog.Default.ResolveConfig(catalog.SSHIntegration, ""); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	options := flag.NewFlagSet("ssh", flag.ContinueOnError)
	options.SetOutput(io.Discard)
	host := options.String(catalog.SSHHostOption, "", "")
	user := options.String(catalog.SSHUserOption, "", "")
	if err := options.Parse(args); err != nil || options.NArg() != 0 {
		fmt.Fprintln(stderr, "ssh requires --host HOST --user USER")
		return 2
	}
	target, err := catalog.SSHDestination(*host, *user)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if !verifyAgent(effects, stderr) {
		return 1
	}
	if effects.HomeDirectory == nil {
		fmt.Fprintln(stderr, "SSH home data is unavailable")
		return 1
	}
	home, err := effects.HomeDirectory()
	if err != nil || !filepath.IsAbs(home) {
		fmt.Fprintln(stderr, "SSH home data is unavailable")
		return 1
	}
	name := fmt.Sprintf("id_ed25519_remote_%x", sha256.Sum256([]byte(target)))
	path := filepath.Join(home, ".ssh", name)
	public, err := prepareSSHKey(home, name, effects)
	// A complete or partial generated key is never removed by this stage, including on failure.
	fmt.Fprintf(stderr, "Dedicated SSH key location: %s\n", path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := fmt.Fprintln(stdout, public); err != nil {
		fmt.Fprintln(stderr, "cannot print SSH public key; dedicated key retained")
		return 1
	}
	stdin := effects.Stdin
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	approved, err := awaitSSHApproval(stdin, *user, target, stderr, effects)
	if err != nil {
		fmt.Fprintf(stderr, "\n%v; no connection made, no host entry written, dedicated key retained at %s\n", err, path)
		return 1
	}
	if !approved {
		fmt.Fprintf(stderr, "SSH connection test declined; no connection made, no host entry written, dedicated key retained at %s\n", path)
		return 1
	}
	if err := testSSHConnection(home, target, *user, stderr, effects); err != nil {
		fmt.Fprintf(stderr, "%v; no host entry written, SSH configuration unchanged, dedicated key retained at %s\n", err, path)
		return 1
	}
	alias, err := configureSSHAlias(target, *user, name, stderr, effects)
	if err != nil {
		fmt.Fprintf(stderr, "%v; dedicated key retained at %s\n", err, path)
		return 1
	}
	fmt.Fprintf(stderr, "SSH alias %s configured for %s@%s with the dedicated key. The password test succeeded; key-based login is not verified. Add the public key above to the remote account's ~/.ssh/authorized_keys before you use ssh %s.\n", alias, *user, target, alias)
	return 0
}

// prepareSSHKey creates a private directory, serializes key generation or reuse,
// and validates the pair before returning public material without changing SSH configuration or trust.
func prepareSSHKey(home, name string, effects Effects) (string, error) {
	root, err := os.OpenRoot(home)
	if err != nil {
		return "", fmt.Errorf("cannot open SSH home data")
	}
	defer root.Close()
	if err := prepareSSHDirectory(root); err != nil {
		return "", err
	}
	lock := filepath.Join(".ssh", "."+name+".lock")
	if err := root.Mkdir(lock, 0700); err != nil {
		return "", fmt.Errorf("SSH key generation is already in progress or its lock is unavailable; retry after resolving the lock")
	}
	defer root.Remove(lock)
	relative := filepath.Join(".ssh", name)
	_, privateErr := root.Lstat(relative)
	_, publicErr := root.Lstat(relative + ".pub")
	if privateErr == nil && publicErr == nil {
		return validateSSHKey(root, home, relative, effects)
	}
	if !errors.Is(privateErr, os.ErrNotExist) || !errors.Is(publicErr, os.ErrNotExist) {
		return "", errUnsafeSSHKey
	}
	path := filepath.Join(home, relative)
	if err := effects.Start(Process{Program: sshKeygenProgram, Args: []string{"-q", "-t", "ed25519", "-N", "", "-C", "", "-f", path}, Identity: agentIdentity, EnvironmentPolicy: SSHEnvironment, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		return "", fmt.Errorf("SSH key generation failed; any generated key material is retained")
	}
	return validateSSHKey(root, home, relative, effects)
}

// readSSHPublic validates file types, ownership, private permissions, and one Ed25519 public line without printing file contents on failure.
func readSSHPublic(root *os.Root, relative string) (string, error) {
	for _, suffix := range []string{"", ".pub"} {
		info, err := root.Lstat(relative + suffix)
		if err != nil || !info.Mode().IsRegular() || !sshFileOwned(info) || info.Mode().Perm()&0022 != 0 || (suffix == "" && info.Mode().Perm() != 0600 && info.Mode().Perm() != 0400) {
			return "", errUnsafeSSHKey
		}
	}
	file, err := root.Open(relative + ".pub")
	if err != nil {
		return "", fmt.Errorf("cannot read SSH public key")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, sshPublicLimit+1))
	if err != nil || len(data) > sshPublicLimit {
		return "", fmt.Errorf("invalid SSH public key; no key overwritten")
	}
	public, err := sshkey.Ed25519(string(data))
	if err != nil {
		return "", fmt.Errorf("invalid SSH public key; no key overwritten")
	}
	return public, nil
}

// prepareSSHDirectory creates the agent's .ssh directory with mode 0700 when it is missing and refuses one that is
// not a real directory owned by agent with exactly that mode; the key and alias stages share it.
func prepareSSHDirectory(root *os.Root) error {
	if err := root.Mkdir(".ssh", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("cannot create SSH directory")
	}
	return checkSSHDirectory(root)
}

// checkSSHDirectory refuses a .ssh directory that is not a real directory owned by agent with mode 0700, without creating one.
func checkSSHDirectory(root *os.Root) error {
	info, err := root.Lstat(".ssh")
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !sshFileOwned(info) {
		return fmt.Errorf("~/.ssh must be a directory, not a link, owned by agent with mode 0700; no files changed")
	}
	return nil
}

// sshOutputBuffer bounds untrusted output of a local SSH process to limit bytes without exposing excess data
// or retaining private material in diagnostics.
type sshOutputBuffer struct {
	limit    int
	data     []byte
	overflow bool
}

// Write captures output up to the limit, records whether any was dropped, and drains the rest.
func (b *sshOutputBuffer) Write(data []byte) (int, error) {
	n := len(data)
	if len(data) > b.limit-len(b.data) {
		data = data[:b.limit-len(b.data)]
		b.overflow = true
	}
	b.data = append(b.data, data...)
	return n, nil
}

// validateSSHKey checks that the private key derives exactly the stored public key before returning public material.
func validateSSHKey(root *os.Root, home, relative string, effects Effects) (string, error) {
	public, err := readSSHPublic(root, relative)
	if err != nil {
		return "", err
	}
	derived := sshOutputBuffer{limit: sshPublicLimit}
	err = effects.Start(Process{Program: sshKeygenProgram, Args: []string{"-y", "-P", "", "-f", filepath.Join(home, relative)}, Identity: agentIdentity, EnvironmentPolicy: SSHEnvironment, Stdin: strings.NewReader(""), Stdout: &derived, Stderr: io.Discard})
	if err != nil || derived.overflow {
		return "", fmt.Errorf("SSH private key is invalid or unavailable; no key overwritten")
	}
	actual, err := sshkey.Ed25519(string(derived.data))
	if err != nil || actual != public {
		return "", fmt.Errorf("SSH private and public keys do not match; no key overwritten")
	}
	return public, nil
}
