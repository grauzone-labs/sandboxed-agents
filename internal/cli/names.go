package cli

import (
	"fmt"
	"regexp"
)

var controllerGroupPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
var sandboxNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

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
	prefix := "sandboxed-agents." + group + "." + name
	return ObjectNames{
		Container: prefix, Backup: "sandboxed-agents-backup." + group + "." + name,
		Workspace: prefix + ".workspace", Home: prefix + ".home", SSH: prefix + ".ssh",
	}
}
