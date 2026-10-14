package cli

import (
	"context"
	"fmt"
	"strings"
	"time"
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

const managerPath = "/usr/local/bin/sandboxed-agents-manager"

// queryManager bounds both the administrative process and any inherited pipes.
func queryManager(p podmanTarget, container, subcommand string) (string, error) {
	args, err := ManagerQueryArgs(container, managerPath, subcommand)
	if err != nil {
		return "", err
	}
	return queryManagerArgs(p, args)
}

func queryManagerSessions(request Request, p podmanTarget) (string, error) {
	return queryManager(p, DerivedNames(request.Group, request.Name).Container, "sessions")
}

func queryManagerArgs(p podmanTarget, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return p.queryContext(ctx, time.Second, args...)
}

func checkManager(p podmanTarget, container string) error {
	answer, err := queryManager(p, container, "version")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(strings.TrimSpace(answer), "sandboxed-agents-manager ") {
		return fmt.Errorf("manager did not answer its version query")
	}
	return nil
}
