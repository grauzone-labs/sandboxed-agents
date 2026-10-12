package cli

import (
	"fmt"
	"strings"
)

// ManagerQueryArgs builds the host's administrative manager query arguments.
// Container ownership and Podman target selection remain the caller's checks.
func ManagerQueryArgs(container, managerPath, subcommand string) ([]string, error) {
	if container == "" || strings.HasPrefix(container, "-") || strings.ContainsAny(container, "\r\n\x00") {
		return nil, fmt.Errorf("invalid manager query container %q", container)
	}
	if !strings.HasPrefix(managerPath, "/") || strings.ContainsAny(managerPath, "\r\n\x00") {
		return nil, fmt.Errorf("manager path must be an absolute container path")
	}
	if subcommand != "version" && subcommand != "sessions" {
		return nil, fmt.Errorf("unknown manager query %q", subcommand)
	}
	return []string{"exec", "--user=0:0", container, managerPath, subcommand}, nil
}
