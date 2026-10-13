package cli

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// OwnerLabel names the authoritative controller group on Podman resources.
const OwnerLabel = "io.sandboxed-agents.owner"

// SandboxNameLabel records the user's sandbox name on its container and volumes.
const SandboxNameLabel = "io.sandboxed-agents.name"

// SSHPortLabel records the host port reserved by a sandbox, even while stopped.
const SSHPortLabel = "io.sandboxed-agents.port"

// defaultSSHPort is the first port considered when no explicit port is requested.
const defaultSSHPort = 2222

func allocatePort(p podmanTarget, requested string) (uint16, error) {
	output, err := p.query("ps", "--all", "--format", "json")
	if err != nil {
		return 0, err
	}
	var records []struct {
		Names  []string
		Labels map[string]string
	}
	if err := json.Unmarshal([]byte(output), &records); err != nil {
		return 0, fmt.Errorf("read recorded SSH ports: %w", err)
	}
	if records == nil {
		return 0, fmt.Errorf("read recorded SSH ports: missing inventory")
	}
	reserved := make(map[uint16]bool)
	for _, record := range records {
		_, managed := record.Labels[OwnerLabel]
		for _, name := range record.Names {
			managed = managed || strings.HasPrefix(name, containerPrefix)
		}
		if !managed {
			continue
		}
		value := record.Labels[SSHPortLabel]
		if value == "" {
			return 0, fmt.Errorf("read recorded SSH ports: sandbox port label is missing")
		}
		port, err := parseSSHPort(value)
		if err != nil {
			return 0, fmt.Errorf("read recorded SSH ports: invalid port %q", value)
		}
		reserved[port] = true
	}
	if requested != "" {
		port, err := parseSSHPort(requested)
		if err != nil {
			return 0, fmt.Errorf("invalid SSH port %q", requested)
		}
		if reserved[port] {
			return 0, fmt.Errorf("SSH port %d is already recorded on a sandbox", port)
		}
		if err := p.host.PortAvailable(port); err != nil {
			return 0, fmt.Errorf("SSH port %d is unavailable on 127.0.0.1: %w", port, err)
		}
		return port, nil
	}
	for port := defaultSSHPort; port <= 65535; port++ {
		if !reserved[uint16(port)] && p.host.PortAvailable(uint16(port)) == nil {
			return uint16(port), nil
		}
	}
	return 0, fmt.Errorf("no SSH port is available between %d and 65535", defaultSSHPort)
}

func parseSSHPort(value string) (uint16, error) {
	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil {
		return 0, err
	}
	if port == 0 {
		return 0, fmt.Errorf("SSH port must be nonzero")
	}
	return uint16(port), nil
}

func loopbackPortAvailable(port uint16) error {
	// This is an availability probe, not a reservation. The command must fail
	// if Podman cannot publish the port; another process can bind after close.
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	if err != nil {
		return err
	}
	return listener.Close()
}
