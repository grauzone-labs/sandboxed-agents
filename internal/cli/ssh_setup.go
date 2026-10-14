package cli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/sshkey"
)

type sshSetup struct {
	groupDir, dir, config, userConfig, entry, identity, knownHosts string
}

func installLifecycleSSHSetup(request Request, p podmanTarget, objects sandboxObjects, stdout io.Writer) error {
	s, err := sshSetupPaths(request, p.host.Platform)
	if err == nil {
		err = installSSHSetup(request, p, objects, s, stdout)
	}
	if err != nil {
		return fmt.Errorf("sandbox %s keeps running; SSH setup failed: %w; retry ssh-config %s --install", request.Name, err, request.Name)
	}
	return nil
}

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
	return sshSetup{groupDir: group, dir: dir, config: filepath.Join(group, "config"), userConfig: filepath.Join(home, ".ssh", "config"), entry: filepath.Join(dir, "entry"), identity: filepath.Join(dir, "id_ed25519"), knownHosts: filepath.Join(dir, "known_hosts")}, nil
}

func sshHostName(request Request) string {
	if request.Group == "default" {
		return request.Name
	}
	return request.Name + "." + request.Group
}

func sshPath(path string) string {
	return quoteSSHPath(strings.ReplaceAll(path, "%", "%%"))
}

func quoteSSHPath(path string) string {
	if filepath.Separator == '\\' {
		path = filepath.ToSlash(path)
	}
	return `"` + strings.ReplaceAll(path, `"`, `\"`) + `"`
}

func (s sshSetup) hostEntry(request Request, objects sandboxObjects) (string, error) {
	port, err := parseSSHPort(objects.container.Config.Labels[SSHPortLabel])
	if err != nil {
		return "", fmt.Errorf("sandbox %s: invalid recorded SSH port: %w", request.Name, err)
	}
	return fmt.Sprintf("Host %s\n    HostName 127.0.0.1\n    Port %d\n    User agent\n    IdentityFile %s\n    UserKnownHostsFile %s\n    GlobalKnownHostsFile none\n    IdentitiesOnly yes\n    IdentityAgent none\n    ForwardAgent no\n    StrictHostKeyChecking yes\n", sshHostName(request), port, sshPath(s.identity), sshPath(s.knownHosts)), nil
}

func configureSSH(request Request, p podmanTarget, stdout, stderr io.Writer) error {
	objects, err := inspectSandbox(request, p)
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
		if _, err := sshManagerQuery(request, p, "ssh-deauthorize"); err != nil {
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

func (s sshSetup) removeHostSetup() error {
	if err := os.RemoveAll(s.dir); err != nil {
		return err
	}
	return s.updateConfiguration()
}

func sshManagerQuery(request Request, p podmanTarget, command string, args ...string) (string, error) {
	argv := []string{"exec", "--user=0:0", DerivedNames(request.Group, request.Name).Container, "/usr/local/bin/sandboxed-agents-manager", command}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return p.queryContext(ctx, time.Second, append(argv, args...)...)
}

func requireSSHManager(request Request, p podmanTarget) error {
	if _, err := sshManagerQuery(request, p, "version"); err != nil {
		return fmt.Errorf("sandbox %s: manager does not answer; use check %s and restart %s: %w", request.Name, request.Name, request.Name, err)
	}
	return nil
}

func installSSHSetup(request Request, p podmanTarget, objects sandboxObjects, s sshSetup, stdout io.Writer) error {
	if err := requireSSHManager(request, p); err != nil {
		return err
	}
	text, err := sshManagerQuery(request, p, "ssh-host-key")
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
		fmt.Fprintf(stdout, "SSH setup for %s is unchanged\n", request.Name)
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := s.checkConflict(sshHostName(request)); err != nil {
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
	if _, err := sshManagerQuery(request, p, "ssh-authorize", publicKey); err != nil {
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
	if err := s.updateConfiguration(); err != nil {
		os.RemoveAll(s.dir)
		return err
	}
	fmt.Fprintf(stdout, "installed SSH setup for %s; connect with ssh %s\n", request.Name, sshHostName(request))
	return nil
}

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
		return string(output), nil
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
