package cli

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/sshkey"
)

// sshSetup holds one sandbox's managed SSH paths, the user's SSH configuration
// path, and the controller group whose locks directory serialises changes to
// the group's shared SSH configuration.
type sshSetup struct {
	group, locks, groupDir, dir, config, userConfig, entry, identity, knownHosts string
}

// installLifecycleSSHSetup installs opt-in SSH setup after a lifecycle action and adds a retry
// diagnostic on failure.
func installLifecycleSSHSetup(request Request, p podmanTarget, objects sandboxObjects, stdout io.Writer) error {
	s, err := sshSetupPaths(request, p.host.Platform)
	if err == nil {
		err = installSSHSetup(request, p, objects, s, stdout)
	}
	if err != nil {
		return lifecycleSSHSetupFailure(request, err)
	}
	return nil
}

// lifecycleSSHSetupFailure reports SSH setup failure while explaining that the sandbox remains
// running.
func lifecycleSSHSetupFailure(request Request, err error) error {
	return fmt.Errorf("sandbox %s keeps running; SSH setup failed: %w; retry ssh-config %s --install", request.Name, err, request.Name)
}

// sshSetupPaths derives this sandbox's managed SSH paths and the user's SSH configuration path.
func sshSetupPaths(request Request, platform string) (sshSetup, error) {
	root, err := hostStateRoot(platform)
	if err != nil {
		return sshSetup{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return sshSetup{}, err
	}
	group := filepath.Join(root, "group-"+request.Group, "ssh")
	hash := sha256.Sum256([]byte(request.Name))
	dir := filepath.Join(group, fmt.Sprintf("sandbox-%x", hash))
	return sshSetup{group: request.Group, locks: filepath.Join(root, "group-"+request.Group, "locks"), groupDir: group, dir: dir, config: filepath.Join(group, "config"), userConfig: filepath.Join(home, ".ssh", "config"), entry: filepath.Join(dir, "entry"), identity: filepath.Join(dir, "id_ed25519"), knownHosts: filepath.Join(dir, "known_hosts")}, nil
}

// sshHostName returns the sandbox's SSH alias, qualifying it with any nondefault controller
// group.
func sshHostName(request Request) string {
	if request.Group == "default" {
		return request.Name
	}
	return request.Name + "." + request.Group
}

// tokenExpandedSSHPath quotes paths for IdentityFile and UserKnownHostsFile,
// where OpenSSH expands percent tokens and a literal percent is written as %%.
func tokenExpandedSSHPath(path string) string {
	return literalSSHPath(strings.ReplaceAll(path, "%", "%%"))
}

// literalSSHPath quotes a path without percent-token escaping.
func literalSSHPath(path string) string {
	if filepath.Separator == '\\' {
		path = filepath.ToSlash(path)
	}
	return `"` + strings.ReplaceAll(path, `"`, `\"`) + `"`
}

// clientIncludeLine selects the Include spelling the installed OpenSSH client
// needs, querying its version only when the path contains a percent sign.
func (s sshSetup) clientIncludeLine() (string, error) {
	if !strings.Contains(s.config, "%") {
		return s.includeLine(false), nil
	}
	output, err := exec.Command("ssh", "-V").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("SSH client version query: %w: %s", err, strings.TrimSpace(string(output)))
	}
	version, ok := strings.CutPrefix(strings.TrimSpace(string(output)), "OpenSSH_")
	version = strings.TrimPrefix(version, "for_Windows_")
	var major, minor int
	if n, _ := fmt.Sscanf(version, "%d.%d", &major, &minor); !ok || n != 2 || major < 1 || minor < 0 {
		return "", fmt.Errorf("cannot determine SSH Include syntax from client version %q", strings.TrimSpace(string(output)))
	}
	// OpenSSH 9.9 added percent expansion to Include; older clients treat
	// both percent signs in %% literally and would miss the managed file.
	return s.includeLine(major > 9 || major == 9 && minor >= 9), nil
}

// hostEntry renders a pinned SSH host entry using the sandbox's recorded loopback port and
// dedicated identity.
func (s sshSetup) hostEntry(request Request, objects sandboxObjects) (string, error) {
	port, err := parseSSHPort(objects.container.Config.Labels[SSHPortLabel])
	if err != nil {
		return "", fmt.Errorf("sandbox %s: invalid recorded SSH port: %w", request.Name, err)
	}
	return fmt.Sprintf("Host %s\n    HostName 127.0.0.1\n    Port %d\n    User agent\n    IdentityFile %s\n    UserKnownHostsFile %s\n    GlobalKnownHostsFile none\n    IdentitiesOnly yes\n    IdentityAgent none\n    ForwardAgent no\n    StrictHostKeyChecking yes\n", sshHostName(request), port, tokenExpandedSSHPath(s.identity), tokenExpandedSSHPath(s.knownHosts)), nil
}

// configureSSH previews, installs, or removes the requested sandbox's opt-in SSH setup.
func configureSSH(request Request, p podmanTarget, stdout, stderr io.Writer) error {
	objects, err := requireSandbox(request, p)
	if err != nil {
		return err
	}
	s, err := sshSetupPaths(request, p.host.Platform)
	if err != nil {
		return err
	}
	if request.Options["remove"] != "" {
		if err := s.removeHostSetup(); err != nil {
			return err
		}
		if !objects.exists || objects.container.State.Running != nil && !*objects.container.State.Running {
			fmt.Fprintf(stdout, "removed SSH setup for %s; authorization remains in the sandbox's SSH server state and is replaced by the next ssh-config %s --install\n", request.Name, request.Name)
			return nil
		}
		if _, err := requireRunning(request, objects); err != nil {
			return err
		}
		if err := requireSSHManager(request, p); err != nil {
			return fmt.Errorf("host SSH setup removed; authorization in sandbox %s could not be removed: %w", request.Name, err)
		}
		if _, err := requestManager(request, p, "ssh-deauthorize"); err != nil {
			return fmt.Errorf("host SSH setup removed; authorization in sandbox %s could not be removed: %w", request.Name, err)
		}
		fmt.Fprintf(stdout, "removed SSH setup and sandbox authorization for %s\n", request.Name)
		return nil
	}
	if request.Options["install"] != "" {
		if _, err := requireRunning(request, objects); err != nil {
			return err
		}
		return installSSHSetup(request, p, objects, s, stdout)
	}
	entry, err := os.ReadFile(s.entry)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		_, err = stdout.Write(entry)
		return err
	}
	prospective, err := s.hostEntry(request, objects)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "SSH setup is not installed; the entry works after ssh-config %s --install\n", request.Name)
	_, err = io.WriteString(stdout, prospective)
	return err
}

// lockConfiguration takes the controller group's SSH configuration lock without
// waiting, refusing with a retry error while another command holds it. The
// caller already holds the sandbox's lifecycle lock and closes the returned
// file to release the group lock.
func (s sshSetup) lockConfiguration() (*os.File, error) {
	return acquireSSHConfigLock(s.locks, s.group)
}

// removeHostSetup deletes this sandbox's host files and refreshes the group's
// managed configuration, removing its Include when no host entries remain. It
// holds the group's SSH configuration lock throughout and changes nothing when
// that lock is busy.
func (s sshSetup) removeHostSetup() error {
	lock, err := s.lockConfiguration()
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := os.RemoveAll(s.dir); err != nil {
		return err
	}
	_, err = s.updateConfiguration("")
	return err
}

// requireSSHManager requires a manager version query to succeed before SSH administration.
func requireSSHManager(request Request, p podmanTarget) error {
	if _, err := requestManager(request, p, "version"); err != nil {
		return fmt.Errorf("sandbox %s: manager does not answer; use check %s and restart %s: %w", request.Name, request.Name, request.Name, err)
	}
	return nil
}

// installSSHSetup authorizes a dedicated key, pins the sandbox's host key, and
// publishes the opt-in host configuration after checking for conflicts. A
// repeat with a matching pin keeps the setup and only restores a missing Include.
// It holds the group's SSH configuration lock from before the first check until
// it returns, and changes nothing when that lock is busy.
func installSSHSetup(request Request, p podmanTarget, objects sandboxObjects, s sshSetup, stdout io.Writer) error {
	lock, err := s.lockConfiguration()
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := requireSSHManager(request, p); err != nil {
		return err
	}
	text, err := requestManager(request, p, "ssh-host-key")
	if err != nil {
		return err
	}
	key, err := sshkey.Ed25519(text)
	if err != nil {
		return fmt.Errorf("sandbox %s: invalid SSH host key: %w", request.Name, err)
	}
	port, err := parseSSHPort(objects.container.Config.Labels[SSHPortLabel])
	if err != nil {
		return err
	}
	pin := "[127.0.0.1]:" + strconv.Itoa(int(port)) + " " + key + "\n"
	if _, err := os.Stat(s.entry); err == nil {
		previous, err := os.ReadFile(s.knownHosts)
		if err != nil {
			return err
		}
		if string(previous) != pin {
			return fmt.Errorf("sandbox %s: SSH host key changed; run ssh-config %s --remove followed by ssh-config %s --install", request.Name, request.Name, request.Name)
		}
		includeLine, err := s.clientIncludeLine()
		if err != nil {
			return err
		}
		restored, err := s.updateConfiguration(includeLine)
		if err != nil {
			return err
		}
		if restored {
			fmt.Fprintf(stdout, "SSH setup for %s is unchanged; restored its Include in the SSH configuration\n", request.Name)
			return nil
		}
		fmt.Fprintf(stdout, "SSH setup for %s is unchanged\n", request.Name)
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := s.checkConflict(sshHostName(request)); err != nil {
		return err
	}
	includeLine, err := s.clientIncludeLine()
	if err != nil {
		return err
	}
	if err := createSSHDirectory(s.groupDir); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(s.groupDir, ".pending-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := secureSSHPath(stage, os.ModeDir|0700); err != nil {
		return err
	}
	identity := filepath.Join(stage, "id_ed25519")
	command := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", identity)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("ssh-keygen: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if err := secureSSHPath(identity, 0600); err != nil {
		return err
	}
	if err := secureSSHPath(identity+".pub", 0644); err != nil {
		return err
	}
	public, err := os.ReadFile(identity + ".pub")
	if err != nil {
		return err
	}
	publicKey, err := sshkey.Ed25519(string(public))
	if err != nil {
		return err
	}
	if _, err := requestManager(request, p, "ssh-authorize", publicKey); err != nil {
		return err
	}
	entry, err := s.hostEntry(request, objects)
	if err != nil {
		return err
	}
	if err := writeSSHFile(filepath.Join(stage, "entry"), []byte(entry), 0600, false); err != nil {
		return err
	}
	if err := writeSSHFile(filepath.Join(stage, "known_hosts"), []byte(pin), 0600, false); err != nil {
		return err
	}
	if err := os.Rename(stage, s.dir); err != nil {
		return err
	}
	if _, err := s.updateConfiguration(includeLine); err != nil {
		os.RemoveAll(s.dir)
		return err
	}
	fmt.Fprintf(stdout, "installed SSH setup for %s; connect with ssh %s\n", request.Name, sshHostName(request))
	return nil
}

// checkConflict refuses an SSH alias whose resolved HostName differs from the client's
// unconfigured default.
func (s sshSetup) checkConflict(host string) error {
	config := s.userConfig
	if _, err := os.Stat(config); os.IsNotExist(err) {
		config = "none"
	} else if err != nil {
		return err
	}
	query := func(file string) (string, error) {
		command := exec.Command("ssh", "-G", "-F", file, host)
		var stderr strings.Builder
		command.Stderr = &stderr
		output, err := command.Output()
		if err != nil {
			return "", fmt.Errorf("SSH configuration query for %s: %w: %s%s", host, err, output, stderr.String())
		}
		// Only a configured HostName claims this host entry. Wildcard defaults
		// for unrelated options may differ without resolving it to another host.
		for _, line := range strings.Split(string(output), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "hostname" {
				return fields[1], nil
			}
		}
		return "", nil
	}
	configured, err := query(config)
	if err != nil {
		return err
	}
	defaults, err := query("none")
	if err != nil {
		return err
	}
	if configured != defaults {
		return fmt.Errorf("SSH host entry %s conflicts with the user's resolved SSH configuration", host)
	}
	return nil
}
