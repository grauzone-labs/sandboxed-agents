package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
)

// setupTokenFile is an explicit bootstrap capability, never part of a public
// page or API. Existing files must be regular and private; secrets are not logged.
func setupTokenFile(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("Standalone -serve/-browser requires -token-file PATH; a private setup token file is read or created (0600)")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		secret := opaqueSecret()
		_, writeErr := file.WriteString(secret + "\n")
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(path)
			return "", fmt.Errorf("Cannot write setup token file")
		}
		return secret, nil
	}
	if !os.IsExist(err) {
		return "", fmt.Errorf("Cannot create setup token file")
	}
	return readTokenFile(path)
}
func readTokenFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("Token file must be a regular file accessible only to its owner (0600)")
	}
	if info.Size() > 4096 {
		return "", fmt.Errorf("Invalid token file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("Cannot read token file")
	}
	secret := strings.TrimSpace(string(data))
	if len(secret) != 64 {
		return "", fmt.Errorf("Token file must contain one 64-character prototype token")
	}
	for _, c := range secret {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return "", fmt.Errorf("Invalid token file")
		}
	}
	return secret, nil
}
func (f *fakeFleet) bootstrapToken(path string) error {
	secret, err := setupTokenFile(path)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	g := &authGrant{info: tokenInfo{ID: opaqueSecret()[:16], Name: "Setup token file / " + filepath.Base(path), Created: now.Format(time.RFC3339), Expires: "Until revoked or process exits", Status: "active"}, hash: sha256.Sum256([]byte(secret)), done: make(chan struct{})}
	f.auth.mu.Lock()
	defer f.auth.mu.Unlock()
	f.auth.grants[g.hash] = g
	f.auth.tokens = append(f.auth.tokens, g)
	return nil
}
func connectionCredential(path string) (string, error) {
	if path != "" {
		return readTokenFile(path)
	}
	if secret := strings.TrimSpace(os.Getenv("SANDBOX_PROTOTYPE_TOKEN")); secret != "" {
		return secret, nil
	}
	fmt.Fprint(os.Stderr, "Backend token (hidden): ")
	secret, err := term.ReadPassword(os.Stdin.Fd())
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("Cannot read hidden token; use -token-file PATH or SANDBOX_PROTOTYPE_TOKEN")
	}
	if len(strings.TrimSpace(string(secret))) == 0 {
		return "", fmt.Errorf("A backend token is required")
	}
	return strings.TrimSpace(string(secret)), nil
}
