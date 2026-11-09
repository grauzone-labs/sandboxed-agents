package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/sshkey"
)

// ManagerQueryArgs builds and validates the host's administrative manager request arguments.
// Container ownership and Podman target selection remain the caller's checks.
func ManagerQueryArgs(container, managerPath, subcommand string, payload ...string) ([]string, error) {
	request, err := newManagerRequest(container, managerPath, subcommand, payload...)
	return request.args, err
}

// managerRequest keeps a validated administrative request together with its retry effect.
// The zero value cannot enable retries.
type managerRequest struct {
	args     []string
	readOnly bool
}

// newManagerRequest defines each administrative operation's payload rules and retry effect
// together, rejecting unknown operations before they can be executed.
func newManagerRequest(container, managerPath, subcommand string, payload ...string) (managerRequest, error) {
	if container == "" || strings.HasPrefix(container, "-") || strings.ContainsAny(container, "\r\n\x00") {
		return managerRequest{}, fmt.Errorf("invalid manager query container %q", container)
	}
	if !strings.HasPrefix(managerPath, "/") || strings.ContainsAny(managerPath, "\r\n\x00") {
		return managerRequest{}, fmt.Errorf("manager path must be an absolute container path")
	}
	var readOnly bool
	switch subcommand {
	case "version", "sessions", "ssh-host-key":
		readOnly = true
		if err := managerNoPayload(subcommand, payload); err != nil {
			return managerRequest{}, err
		}
	case "ssh-deauthorize":
		if err := managerNoPayload(subcommand, payload); err != nil {
			return managerRequest{}, err
		}
	case "ssh-authorize":
		if len(payload) != 1 {
			return managerRequest{}, fmt.Errorf("manager ssh-authorize requires one public key")
		}
		key, err := sshkey.Ed25519(payload[0])
		if err != nil {
			return managerRequest{}, err
		}
		payload = []string{key}
	case "agents":
		readOnly = true
		if !(len(payload) == 1 && payload[0] == "list") && !(len(payload) == 2 && payload[0] == "status") {
			return managerRequest{}, fmt.Errorf("manager agent query requires list or status AGENT")
		}
	case "tools":
		readOnly = true
		if !(len(payload) == 1 && payload[0] == "list") {
			return managerRequest{}, fmt.Errorf("manager tool query requires list")
		}
	default:
		return managerRequest{}, fmt.Errorf("unknown manager query %q", subcommand)
	}
	return managerRequest{args: append([]string{"exec", "--user=0:0", container, managerPath, subcommand}, payload...), readOnly: readOnly}, nil
}

// managerNoPayload rejects arguments to administrative operations that take none.
func managerNoPayload(subcommand string, payload []string) error {
	if len(payload) != 0 {
		return fmt.Errorf("manager %s takes no arguments", subcommand)
	}
	return nil
}

// requestManager sends a validated administrative manager request with bounded execution and
// pipe draining.
func requestManager(request Request, p podmanTarget, subcommand string, payload ...string) (string, error) {
	operation, err := newManagerRequest(DerivedNames(request.Group, request.Name).Container, "/usr/local/bin/sandboxed-agents-manager", subcommand, payload...)
	if err != nil {
		return "", err
	}
	return queryManagerRequest(p, operation)
}

const managerPath = "/usr/local/bin/sandboxed-agents-manager"

// queryManager bounds both the administrative process and any inherited pipes.
func queryManager(p podmanTarget, container, subcommand string) (string, error) {
	operation, err := newManagerRequest(container, managerPath, subcommand)
	if err != nil {
		return "", err
	}
	return queryManagerRequest(p, operation)
}

// queryManagerSessions queries the sandbox's agent sessions through the administrative manager
// interface.
func queryManagerSessions(request Request, p podmanTarget) (string, error) {
	return queryManager(p, DerivedNames(request.Group, request.Name).Container, "sessions")
}

// Administrative requests have one 15-second budget, including a possible retry and pipe
// draining. Readiness retains its independent three-second probes and 60-second deadline.
const managerRequestBudget = 15 * time.Second

// queryManagerRequest executes administration within one total budget. Only explicitly read-only
// requests may retry once after transport, deadline or pipe-completion failure. Failed output is
// never response data, and mutations are never repeated after an ambiguous result.
func queryManagerRequest(p podmanTarget, request managerRequest) (string, error) {
	started := time.Now()
	deadline := started.Add(managerRequestBudget)
	readOnly := request.readOnly
	var last error
	attempts := 0
	for attempts < 2 {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		budget := remaining
		if readOnly && attempts == 0 {
			budget = min(budget, 10*time.Second)
		}
		drain := min(time.Second, budget/5)
		ctx, cancel := context.WithTimeout(context.Background(), budget-drain)
		answer, err := p.queryContext(ctx, drain, request.args...)
		contextErr := ctx.Err()
		cancel()
		attempts++
		if contextErr != nil {
			err = errors.Join(err, contextErr)
		}
		if err == nil {
			return answer, nil
		}
		last = err
		if !readOnly || !managerRetryable(err) || attempts == 2 {
			break
		}
		if time.Until(deadline) <= 100*time.Millisecond {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("manager request failed after %s (%d attempt(s), %s; total budget %s): %w",
		time.Since(started).Round(time.Millisecond), attempts, managerFailureKind(last), managerRequestBudget, last)
}

// managerRetryable recognizes a deadline, incomplete pipe drain or Podman's reserved exec
// failure statuses; a manager operation's own failure and a missing executable are not retried.
func managerRetryable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, exec.ErrWaitDelay) {
		return true
	}
	var exit *exec.ExitError
	return errors.As(err, &exit) && podmanTransportFailure(exit.ExitCode())
}

// managerFailureKind labels only facts established by the process result, never inferring that
// valid output means completion or that a remote manager operation was cancelled.
func managerFailureKind(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline exceeded"
	case errors.Is(err, exec.ErrWaitDelay):
		return "output completion failed"
	case managerOperationFailure(err):
		return "manager operation failed"
	default:
		return "transport failed"
	}
}

// checkManager requires a successful manager version response with the expected product prefix.
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
