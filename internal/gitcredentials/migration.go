package gitcredentials

import (
	"bytes"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// gitCredential runs Git's credential protocol with only the managed helper, never repository or legacy helpers.
func (s Store) gitCredential(operation, record string, useGenericGCMProvider bool) ([]byte, error) {
	helper := s.HelperConfig()
	if useGenericGCMProvider {
		helper += " --generic"
	}
	var out bytes.Buffer
	args := []string{"-c", "credential.helper=", "-c", "credential.helper=" + helper, "-c", "credential.useHttpPath=true", "-c", "credential.interactive=false", "credential", operation}
	err := s.run("git", args, strings.NewReader(record), &out)
	return out.Bytes(), err
}

// migrate imports every parsed legacy URL and verifies username and password through real Git before deleting the source.
func (s Store) migrate() error {
	path := filepath.Join(s.Home, ".git-credentials")
	original, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("cannot read legacy Git credentials; source and helpers unchanged")
	}
	var records []string
	for _, line := range strings.Split(string(original), "\n") {
		if line == "" {
			continue
		}
		u, err := url.Parse(line)
		if err != nil || u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User == nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("invalid legacy Git credential; source and helpers unchanged")
		}
		password, ok := u.User.Password()
		if !ok || u.User.Username() == "" || password == "" || strings.ContainsAny(u.Host+u.Path+u.User.Username()+password, "\r\n\x00") {
			return errors.New("invalid legacy Git credential; source and helpers unchanged")
		}
		record := "protocol=" + u.Scheme + "\nhost=" + u.Host + "\n"
		if u.Path != "" {
			record += "path=" + strings.TrimPrefix(u.Path, "/") + "\n"
		}
		record += "username=" + u.User.Username() + "\npassword=" + password + "\n\n"
		records = append(records, record)
	}
	for _, record := range records {
		if _, err := s.gitCredential("approve", record, true); err != nil {
			return errors.New("legacy Git credential import failed; source and helpers unchanged")
		}
	}
	for _, record := range records {
		// Remove the password from the request so the readback cannot satisfy itself.
		before, passwordLine, _ := strings.Cut(record, "password=")
		data, err := s.gitCredential("fill", before+"\n", true)
		password := strings.TrimSuffix(passwordLine, "\n\n")
		userLine := ""
		for _, line := range strings.Split(before, "\n") {
			if strings.HasPrefix(line, "username=") {
				userLine = line
			}
		}
		if err != nil || !bytes.Contains(data, []byte("password="+password+"\n")) || !bytes.Contains(data, []byte(userLine+"\n")) {
			return errors.New("legacy Git credential readback failed; source and helpers unchanged")
		}
	}
	// Detect a source changed during migration instead of deleting another writer's credentials.
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(current, original) {
		return errors.New("legacy Git credential source changed; source and helpers retained")
	}
	return nil
}

// StoreAzureDevOps pins the organization to GCM's generic provider and approves its PAT through only the managed helper.
func (s Store) StoreAzureDevOps(organization, username, token string) error {
	if err := s.Check(); err != nil {
		return err
	}
	origin, _ := url.Parse(organization)
	for _, setting := range [][2]string{{"credential." + origin.Scheme + "://" + origin.Host + ".provider", "generic"}, {"credential." + organization + ".provider", "generic"}, {"credential." + organization + ".username", username}} {
		if err := s.run("git", []string{"config", "--global", "--replace-all", "--", setting[0], setting[1]}, nil, io.Discard); err != nil {
			return err
		}
	}
	// Azure organization credentials are host-scoped and selected by the per-organization username, matching clone URLs with repository paths.
	args := []string{"-c", "credential.helper=", "-c", "credential.helper=" + s.HelperConfig(), "-c", "credential.useHttpPath=false", "credential"}
	if err := s.run("git", append(args, "approve"), strings.NewReader("url="+organization+"\nusername="+username+"\npassword="+token+"\n\n"), io.Discard); err != nil {
		return err
	}
	var result bytes.Buffer
	if err := s.run("git", append(args, "fill"), strings.NewReader("url="+organization+"\nusername="+username+"\n\n"), &result); err != nil {
		return errors.New("Azure DevOps Git credential readback failed")
	}
	if credentialField(result.String(), "password") != token || credentialField(result.String(), "username") != username {
		return errors.New("Azure DevOps Git credential readback did not match")
	}
	return nil
}

// configure stages global settings under Git's exclusive lock and removes verified plaintext only after publication.
func (s Store) configure() error {
	config := filepath.Join(s.Home, ".gitconfig")
	original, readErr := os.ReadFile(config)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return errors.New("cannot read global Git configuration")
	}
	mode := os.FileMode(0600)
	if info, err := os.Stat(config); err == nil {
		mode = info.Mode().Perm()
	}
	lock := config + ".lock"
	file, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return errors.New("global Git configuration is locked; credentials and helpers retained")
	}
	defer os.Remove(lock)
	if _, err := file.Write(original); err != nil {
		file.Close()
		return errors.New("cannot stage Git configuration")
	}
	if err := file.Close(); err != nil {
		return errors.New("cannot close staged Git configuration")
	}
	for _, setting := range [][2]string{{"credential.helper", s.HelperConfig()}, {"credential.credentialStore", "gpg"}, {"credential.interactive", "false"}} {
		if err := s.run("git", []string{"config", "--file", lock, "--replace-all", "--", setting[0], setting[1]}, nil, io.Discard); err != nil {
			return err
		}
	}
	if err := s.migrate(); err != nil {
		return err
	}
	if err := os.Rename(lock, config); err != nil {
		return errors.New("cannot publish global Git configuration; legacy credentials retained")
	}
	if err := os.Remove(filepath.Join(s.Home, ".git-credentials")); err != nil && !errors.Is(err, os.ErrNotExist) {
		var restoreErr error
		if errors.Is(readErr, os.ErrNotExist) {
			restoreErr = os.Remove(config)
		} else {
			restoreErr = os.WriteFile(config, original, mode)
		}
		if restoreErr != nil {
			return errors.New("legacy Git credentials could not be removed and Git configuration could not be restored; manual recovery required")
		}
		return errors.New("legacy Git credentials could not be removed; global configuration restored")
	}
	return nil
}

// credentialField reads an exact Git credential protocol field without matching data inside another value.
func credentialField(record, name string) string {
	for _, line := range strings.Split(record, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && key == name {
			return value
		}
	}
	return ""
}
