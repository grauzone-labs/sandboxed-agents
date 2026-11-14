package catalog

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// ProxyOptions validates the shared proxy flags and returns a normalized server base URL.
func ProxyOptions(options map[string]string) (string, error) {
	_, modelSet := options["model"]
	if options["remove"] != "" {
		return "", nil
	}
	if modelSet && (options["model"] == "" || strings.IndexFunc(options["model"], unicode.IsControl) >= 0) {
		return "", fmt.Errorf("--model requires a nonempty model ID without controls")
	}
	raw := options["url"]
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.ForceQuery || u.RawQuery != "" || strings.Contains(raw, "#") || strings.IndexFunc(raw, unicode.IsSpace) >= 0 || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("--url requires an HTTP or HTTPS base URL without credentials, query or fragment")
	}
	if strings.IndexFunc(u.Path, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("--url path contains controls")
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("--url has invalid port")
		}
	}
	escapedPath := strings.TrimRight(u.EscapedPath(), "/")
	u.Path, err = url.PathUnescape(escapedPath)
	if err != nil {
		return "", fmt.Errorf("--url has invalid path")
	}
	u.RawPath = escapedPath
	if strings.HasSuffix(u.Path, "/v1") {
		return "", fmt.Errorf("--url must not end in /v1")
	}
	return u.String(), nil
}

// ProxyUnencrypted reports whether the URL sends credentials over non-loopback HTTP.
func ProxyUnencrypted(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	return !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback())
}

// ReadProxyKey reads only the first line, strips surrounding whitespace and rejects control or internal whitespace.
func ReadProxyKey(input io.Reader) (string, error) {
	if input == nil {
		return "", fmt.Errorf("proxy key is required")
	}
	raw, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("cannot read proxy key")
	}
	raw = strings.TrimSpace(raw)
	if strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("proxy key must not contain control characters")
	}
	if raw == "" || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("proxy key is required and must not contain whitespace")
	}
	return raw, nil
}

// ProxyOptions validates this agent's catalog workflow before normalizing its server URL.
func (a Agent) ProxyOptions(options map[string]string) (string, error) {
	if err := a.LoginWorkflows["proxy"].ValidateOptions(options); err != nil {
		return "", err
	}
	return ProxyOptions(options)
}

// codexProxyMinimumMinor is the oldest tested native API-key login contract.
const codexProxyMinimumMinor = 166

// CodexProxyVersion refuses clients older than the tested native authentication contract.
func CodexProxyVersion(version string) error {
	var major, minor, patch int
	n, err := fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch)
	validRelease := ValidateVersion(version) == nil && !strings.Contains(version, "-") && err == nil && n == 3
	if !validRelease || major == 0 && minor < codexProxyMinimumMinor {
		return fmt.Errorf("Codex proxy requires Codex 0.%d.0 or newer; run agents update or agents enable --version", codexProxyMinimumMinor)
	}
	return nil
}

// CopilotProxyVersion refuses versions below the tested stable native-provider registry floor.
func CopilotProxyVersion(version string) error {
	base, _, _ := strings.Cut(version, "+")
	if ValidateVersion(version) == nil && !strings.Contains(base, "-") {
		parts := strings.Split(base, ".")
		floor := []string{"1", "0", "106"}
		for i, part := range parts {
			if len(part) > len(floor[i]) || (len(part) == len(floor[i]) && part > floor[i]) {
				return nil
			}
			if len(part) < len(floor[i]) || (len(part) == len(floor[i]) && part < floor[i]) {
				break
			}
			if i == 2 {
				return nil
			}
		}
	}
	return fmt.Errorf("Copilot proxy requires version 1.0.106 or newer (stable); use agents update NAME copilot --unpin or agents enable NAME copilot --version 1.0.106")
}
