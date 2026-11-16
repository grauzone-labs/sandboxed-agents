package gitcredentials

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Store identifies the dedicated home state and trusted manager executable, without holding a passphrase.
type Store struct {
	Home, Manager string
	generic       bool
}

// directory is the product's dedicated GnuPG home and pass store parent.
func (s Store) directory() string {
	return filepath.Join(s.Home, ".local/share/sandboxed-agents/git-credentials")
}

// gpgHome isolates the key and agent from the user's other GnuPG keys.
func (s Store) gpgHome() string { return filepath.Join(s.directory(), "gnupg") }

// passwordStore is the pass-compatible directory containing only encrypted credentials and its recipient.
func (s Store) passwordStore() string { return filepath.Join(s.directory(), "password-store") }

// environment allows only known nonsecret settings, disabling inherited GCM, Git and GnuPG tracing and overrides.
func (s Store) environment() []string {
	env := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + s.Home, "USER=agent", "LOGNAME=agent", "LANG=C.UTF-8", "GNUPGHOME=" + s.gpgHome(), "PASSWORD_STORE_DIR=" + s.passwordStore(), "GPG_TTY=/dev/null", "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GCM_CREDENTIAL_STORE=gpg", "GCM_GUI_PROMPT=0", "GCM_AUTODETECT_PROVIDER=false", "GIT_CONFIG_GLOBAL=" + filepath.Join(s.Home, ".gitconfig"), "XDG_CONFIG_HOME=" + filepath.Join(s.Home, ".config")}
	if s.generic {
		env = append(env, "GCM_PROVIDER=generic")
	}
	return env
}

// keys reads the dedicated public key and validates every corresponding protected private key file.
func (s Store) keys() (fingerprint string, grips []string, err error) {
	var out bytes.Buffer
	if err = s.run("gpg", []string{"--no-options", "--batch", "--no-auto-check-trustdb", "--with-colons", "--with-keygrip", "--list-keys"}, nil, &out); err != nil {
		return
	}
	primary, sub, pending := 0, 0, ""
	for _, line := range strings.Split(out.String(), "\n") {
		fields := strings.Split(line, ":")
		switch fields[0] {
		case "pub", "sub":
			if len(fields) < 12 || fields[1] == "r" || fields[1] == "e" || fields[6] != "" {
				return "", nil, errors.New("dedicated Git credential key is incomplete or expired")
			}
			if fields[0] == "pub" {
				primary++
				pending = "primary"
			} else {
				sub++
				pending = "sub"
				if !strings.Contains(fields[11], "e") {
					return "", nil, errors.New("dedicated key lacks encryption subkey")
				}
			}
		case "fpr":
			if pending == "primary" && fingerprint == "" && len(fields) > 9 {
				fingerprint = fields[9]
			}
		case "grp":
			if len(fields) > 9 {
				grip := fields[9]
				if len(grip) != 40 || strings.Trim(grip, "0123456789ABCDEF") != "" {
					return "", nil, errors.New("invalid dedicated keygrip")
				}
				data, readErr := os.ReadFile(filepath.Join(s.gpgHome(), "private-keys-v1.d", grip+".key"))
				if readErr != nil || !bytes.Contains(data, []byte("protected-private-key")) {
					return "", nil, errors.New("dedicated private key is missing or damaged")
				}
				grips = append(grips, grip)
			}
		}
	}
	if primary != 1 || sub != 1 || len(grips) != 2 || len(fingerprint) != 40 {
		return "", nil, errors.New("expected one dedicated primary key and encryption subkey")
	}
	return
}

// State reports absent or structurally valid managed state without creating or repairing it.
func (s Store) State() (string, error) {
	_, err := os.Lstat(s.directory())
	if errors.Is(err, os.ErrNotExist) {
		return "new", nil
	}
	if err != nil {
		return "", errors.New("cannot inspect Git credential store")
	}
	for _, path := range []string{s.gpgHome(), s.passwordStore(), filepath.Join(s.gpgHome(), "pubring.kbx"), filepath.Join(s.gpgHome(), "trustdb.gpg"), filepath.Join(s.gpgHome(), "gpg-agent.conf")} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("Git credential store has missing or damaged state; manual recovery required")
		}
	}

	config, err := os.ReadFile(filepath.Join(s.gpgHome(), "gpg-agent.conf"))
	if err != nil {
		return "", errors.New("cannot read dedicated agent configuration")
	}
	pinentry := false
	for _, line := range strings.Split(string(config), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if line == "pinentry-program "+filepath.Join(filepath.Dir(s.Manager), "sandboxed-agents-pinentry") {
			pinentry = true
			continue
		}
		if line == "no-allow-external-cache" {
			continue
		}
		if len(fields) == 2 && (fields[0] == "default-cache-ttl" || fields[0] == "max-cache-ttl") {
			value, err := strconv.ParseUint(fields[1], 10, 32)
			if err == nil && value > 0 {
				continue
			}
		}
		return "", errors.New("dedicated Git credential agent configuration is damaged or enables unsupported logging or prompting")
	}
	if !pinentry {
		return "", errors.New("dedicated Git credential pinentry is missing")
	}
	recipient, err := os.ReadFile(filepath.Join(s.passwordStore(), ".gpg-id"))
	if err != nil {
		return "", errors.New("Git credential store recipient is missing")
	}
	fingerprint, _, err := s.keys()
	if err != nil {
		return "", err
	}
	if string(recipient) != fingerprint+"\n" {
		return "", errors.New("Git credential store recipient does not match its dedicated key")
	}
	// Nested recipients could silently send newly stored credentials to another key.
	err = filepath.WalkDir(s.passwordStore(), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != s.passwordStore() && (d.Type()&os.ModeSymlink != 0 || d.Name() == ".gpg-id" && path != filepath.Join(s.passwordStore(), ".gpg-id")) {
			return errors.New("Git credential store has an unexpected recipient or symlink")
		}
		return nil
	})
	if err != nil {
		return "", errors.New("Git credential store is damaged")
	}
	return "ready", nil
}

// Lock ends only the dedicated agent, clearing generic memory and all warm key caches.
func (s Store) Lock() error {
	socket, err := s.AgentSocket()
	if err != nil {
		return err
	}
	return s.terminateAgent(socket)
}

// agent starts the dedicated gpg-agent without IPC logging and opens its private socket.
func (s Store) agent() (*agentConnection, error) {
	if err := s.run("gpgconf", []string{"--launch", "gpg-agent"}, nil, io.Discard); err != nil {
		return nil, err
	}
	socket, err := s.AgentSocket()
	if err != nil {
		return nil, err
	}
	a, err := connectAgent(socket)
	if err != nil {
		return nil, err
	}
	if err := s.rememberAgent(a); err != nil {
		a.Close()
		return nil, err
	}
	return a, nil
}

// Check requires valid state and the product's generic-memory unlock without consuming credentials.
func (s Store) Check() error {
	state, err := s.State()
	if err != nil {
		return err
	}
	if state != "ready" {
		return errors.New("Git credential store is not set up")
	}
	socket, err := s.AgentSocket()
	if err != nil {
		return err
	}
	a, err := connectAgent(socket)
	if err != nil {
		return err
	}
	defer a.Close()
	secret, err := a.command("GET_SECRET "+secretName, nil)
	clear(secret)
	if err != nil || len(secret) == 0 {
		return errors.New("Git credential store is locked")
	}
	return nil
}

// Setup creates a protected Ed25519 primary and Curve25519 encryption subkey, or verifies and unlocks the existing store.
func (s Store) Setup(passphrase, confirmation []byte) error {

	// Relock existing managed state before any GPG inspection can wait on a stalled agent.
	if info, err := os.Lstat(s.directory()); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("Git credential store directory is damaged")
		}
		if gpgInfo, err := os.Lstat(s.gpgHome()); err == nil && gpgInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("dedicated GnuPG home is damaged")
		}
		if err := s.Lock(); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect Git credential store")
	}
	state, err := s.State()
	if err != nil {
		return err
	}
	if state == "new" {
		if len(passphrase) == 0 || !bytes.Equal(passphrase, confirmation) {
			return errors.New("empty or mismatched passphrase; no configuration changed")
		}
	}
	if len(passphrase) == 0 || bytes.ContainsAny(passphrase, "\r\n\x00") || len(passphrase) > 256 {
		return errors.New("invalid Git credential passphrase")
	}
	if state == "new" {
		if err := os.MkdirAll(s.gpgHome(), 0700); err != nil {
			return errors.New("cannot create dedicated GnuPG home")
		}
		config := "pinentry-program " + filepath.Join(filepath.Dir(s.Manager), "sandboxed-agents-pinentry") + "\nno-allow-external-cache\n"
		if err := os.WriteFile(filepath.Join(s.gpgHome(), "gpg-agent.conf"), []byte(config), 0600); err != nil {
			return errors.New("cannot configure dedicated gpg-agent")
		}
		if err := s.run("gpg", []string{"--no-options", "--batch", "--pinentry-mode", "loopback", "--passphrase-fd", "0", "--quick-generate-key", "Sandboxed Agents Git credential store", "ed25519", "cert", "0"}, bytes.NewReader(append(bytes.Clone(passphrase), '\n')), io.Discard); err != nil {
			return err
		}
		var out bytes.Buffer
		if err := s.run("gpg", []string{"--no-options", "--batch", "--with-colons", "--list-keys"}, nil, &out); err != nil {
			return err
		}
		fingerprint := ""
		for _, line := range strings.Split(out.String(), "\n") {
			f := strings.Split(line, ":")
			if f[0] == "fpr" && len(f) > 9 {
				fingerprint = f[9]
				break
			}
		}
		if fingerprint == "" {
			return errors.New("cannot identify dedicated Git credential key")
		}
		if err := s.run("gpg", []string{"--no-options", "--batch", "--pinentry-mode", "loopback", "--passphrase-fd", "0", "--quick-add-key", fingerprint, "cv25519", "encr", "0"}, bytes.NewReader(append(bytes.Clone(passphrase), '\n')), io.Discard); err != nil {
			return err
		}
		if err := os.MkdirAll(s.passwordStore(), 0700); err != nil {
			return errors.New("cannot create Git credential store")
		}
		if err := os.WriteFile(filepath.Join(s.passwordStore(), ".gpg-id"), []byte(fingerprint+"\n"), 0600); err != nil {
			return errors.New("cannot initialize Git credential store")
		}
	}
	_, grips, err := s.keys()
	if err != nil {
		return err
	}
	// A fresh agent clears generation and prior ordinary caches before the independent verification.
	if err := s.Lock(); err != nil {
		return err
	}
	a, err := s.agent()
	if err != nil {
		return err
	}
	defer a.Close()
	if _, err = a.command("OPTION pinentry-mode=loopback", nil); err != nil {
		return err
	}
	for _, grip := range grips {
		if _, err = a.command("PASSWD --verify "+grip, passphrase); err != nil {
			s.Lock()
			return errors.New("incorrect passphrase or damaged Git credential key; store remains locked")
		}
	}
	if _, err = a.command("PUT_SECRET "+secretName+" -1 "+escapeAssuan(string(passphrase)), nil); err != nil {
		s.Lock()
		return err
	}
	return s.configure()
}

// HelperConfig is Git's shell-helper form, quoting the trusted manager's executable path.
func (s Store) HelperConfig() string {
	return "!'" + strings.ReplaceAll(s.Manager, "'", "'\"'\"'") + "' __git-credential-helper"
}

// AgentSocket asks GnuPG for the dedicated socket, including runtime-directory redirection.
func (s Store) AgentSocket() (string, error) {
	var out bytes.Buffer
	if err := s.run("gpgconf", []string{"--list-dirs", "agent-socket"}, nil, &out); err != nil {
		return "", err
	}
	socket := strings.TrimSpace(out.String())
	if socket == "" {
		return "", errors.New("dedicated gpg-agent socket is unavailable")
	}
	return socket, nil
}
