package cli

import (
	"fmt"
	"regexp"
	"strings"
)

var controllerGroupPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
var sandboxNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

const (
	containerPrefix = "sandboxed-agents."
	backupPrefix    = "sandboxed-agents-backup."
)

// validateNames validates the controller group and any nonempty sandbox name against their
// supported grammar.
func validateNames(group, name string) error {
	if !controllerGroupPattern.MatchString(group) {
		return fmt.Errorf("invalid controller group %q", group)
	}
	if name != "" && !sandboxNamePattern.MatchString(name) {
		return fmt.Errorf("invalid sandbox name %q", name)
	}
	return nil
}

// ObjectNames derives Podman names only; it grants no ownership authority.
type ObjectNames struct {
	Container, Backup, Workspace, Home, SSH string
}

// DerivedNames expects an already validated controller group and sandbox name.
func DerivedNames(group, name string) ObjectNames {
	prefix := containerPrefix + group + "." + name
	return ObjectNames{
		Container: prefix, Backup: backupPrefix + group + "." + name,
		Workspace: prefix + ".workspace", Home: prefix + ".home", SSH: prefix + ".ssh",
	}
}

// sandboxHostname derives the OS hostname and requested tailnet node label from the sandbox
// name: lowercase ASCII letters, digits, and hyphens, with edge hyphens removed and a maximum
// of 63 characters. It uses sandbox when normalization leaves nothing.
func sandboxHostname(name string) string {
	var label strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			label.WriteRune(r)
		} else {
			label.WriteRune('-')
		}
	}
	hostname := strings.Trim(label.String(), "-")
	if len(hostname) > 63 {
		hostname = strings.TrimRight(hostname[:63], "-")
	}
	if hostname == "" {
		hostname = "sandbox"
	}
	return hostname
}
