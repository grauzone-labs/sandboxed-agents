package catalog

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

const (
	// SSHIntegration names the outbound SSH integration, which connects a sandbox to a remote machine with a dedicated key and an alias.
	SSHIntegration = "ssh"
	// SSHHostOption names the remote machine supplied to the SSH integration.
	SSHHostOption = "host"
	// SSHUserOption names the remote account supplied to the SSH integration.
	SSHUserOption = "user"
)

var sshMachineLabel = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
var sshUser = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@\\-]*$`)
var sshZone = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// SSHDestination validates literal connection input without DNS or filesystem access and returns the canonical target.
// Hostname case and equivalent IP spellings share an identity; a DNS root dot and IPv6 zone remain significant.
func SSHDestination(host, user string) (string, error) {
	if !sshUser.MatchString(user) {
		return "", fmt.Errorf("ssh requires a valid --user USER")
	}
	if address, err := netip.ParseAddr(host); err == nil {
		if zone := address.Zone(); zone != "" && !sshZone.MatchString(zone) {
			return "", fmt.Errorf("ssh requires a valid --host HOST")
		}
		return address.String(), nil
	}
	name := strings.TrimSuffix(host, ".")
	if len(name) == 0 || len(name) > 253 {
		return "", fmt.Errorf("ssh requires a valid --host HOST")
	}
	numeric := true
	for _, c := range name {
		if c != '.' && (c < '0' || c > '9') {
			numeric = false
		}
	}
	if numeric {
		return "", fmt.Errorf("ssh requires a valid --host HOST")
	}
	for _, label := range strings.Split(name, ".") {
		if !sshMachineLabel.MatchString(label) {
			return "", fmt.Errorf("ssh requires a valid --host HOST")
		}
	}
	return strings.ToLower(host), nil
}
