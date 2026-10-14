package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/sshkey"
)

// ManagerQueryArgs builds and validates the host's administrative manager request arguments.
// Container ownership and Podman target selection remain the caller's checks.
func ManagerQueryArgs(container, managerPath, subcommand string, payload ...string) ([]string, error) {
	if container == "" || strings.HasPrefix(container, "-") || strings.ContainsAny(container, "\r\n\x00") {
		return nil, fmt.Errorf("invalid manager query container %q", container)
	}
	if !strings.HasPrefix(managerPath, "/") || strings.ContainsAny(managerPath, "\r\n\x00") {
		return nil, fmt.Errorf("manager path must be an absolute container path")
	}
	switch subcommand {
	case "version", "sessions", "ssh-host-key", "ssh-deauthorize":
		if len(payload) != 0 {
			return nil, fmt.Errorf("manager %s takes no arguments", subcommand)
		}
	case "ssh-authorize":
		if len(payload) != 1 {
			return nil, fmt.Errorf("manager ssh-authorize requires one public key")
		}
		key, err := sshkey.Ed25519(payload[0])
		if err != nil {
			return nil, err
		}
		payload = []string{key}
	case "agents":
		if !(len(payload) == 1 && payload[0] == "list") && !(len(payload) == 2 && payload[0] == "status") {
			return nil, fmt.Errorf("manager agent query requires list or status AGENT")
		}
	default:
		return nil, fmt.Errorf("unknown manager query %q", subcommand)
	}
	return append([]string{"exec", "--user=0:0", container, managerPath, subcommand}, payload...), nil
}

func requestManager(request Request, p podmanTarget, subcommand string, payload ...string) (string, error) {
	args, err := ManagerQueryArgs(DerivedNames(request.Group, request.Name).Container, "/usr/local/bin/sandboxed-agents-manager", subcommand, payload...)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Bound inherited output pipes as well as Podman itself: descendants may
	// keep them open after their parent exits. Other Podman queries have no delay.
	return p.queryContext(ctx, time.Second, args...)
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
