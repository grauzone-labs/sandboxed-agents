// Package azurebrowser defines the private callback record shared by the host and manager.
package azurebrowser

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Prefix introduces a JSON-encoded authorization URL in manager terminal output.
const Prefix = "\x1eSANDBOXED_AGENTS_AZURE_BROWSER "

// Port validates an authorization URL and returns its HTTP loopback redirect port.
func Port(address string) (int, error) {
	u, err := url.Parse(address)
	if err != nil || strings.ContainsAny(address, "\r\n\t\x1b\x1e") || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return 0, fmt.Errorf("invalid Azure browser callback")
	}
	redirect := u
	if u.Scheme == "https" {
		redirect, err = url.Parse(u.Query().Get("redirect_uri"))
	}
	if err != nil || redirect.Scheme != "http" || (redirect.Hostname() != "localhost" && redirect.Hostname() != "127.0.0.1") || redirect.User != nil {
		return 0, fmt.Errorf("Azure browser callback has no loopback redirect; cancel login and retry with device-code")
	}
	port, err := strconv.Atoi(redirect.Port())
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("Azure browser callback has an invalid redirect port")
	}
	return port, nil
}
