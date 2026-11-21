package liverun

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// engineFailure carries a fixed public classification while preserving private diagnostics.
type engineFailure struct {
	kind, reason string
	detail       error
}

// Error preserves the original query detail for private diagnostic retention.
func (e *engineFailure) Error() string { return fmt.Sprintf("%s: %s; %v", e.kind, e.reason, e.detail) }

// engineFault attaches an allowlisted classification to a target or query failure.
func engineFault(kind, reason string, err error) error {
	return &engineFailure{kind: kind, reason: reason, detail: err}
}

// engineReason translates recognized engine messages into bounded path-free actionable reasons.
func engineReason(err error) string {
	text := strings.ToLower(fmt.Sprint(err))
	for _, entry := range []struct{ match, reason string }{
		{"connection refused", "connection refused; check the selected Podman service"},
		{"actively refused", "connection refused; check the selected Podman service"},
		{"timed out", "connection timed out; check the selected Podman connection"},
		{"deadline exceeded", "connection timed out; check the selected Podman connection"},
		{"permission denied", "permission denied; check access to the selected Podman connection"},
		{"machine is not running", "machine stopped; start it outside this command"},
		{"no such host", "engine address unavailable; check the selected Podman connection"},
	} {
		if strings.Contains(text, entry.match) {
			return entry.reason
		}
	}
	return "engine did not answer; check the selected Podman connection"
}

// cleanupPendingEngine distinguishes invalid target selection from actual connection failure.
func cleanupPendingEngine(err error) error {
	var fault *engineFailure
	if errors.As(err, &fault) {
		if fault.kind != "engine unreachable" {
			return fmt.Errorf("cleanup: pending (%s: %s; private diagnostic retained)", fault.kind, fault.reason)
		}
		return &cleanupEngineError{reason: fault.reason}
	}
	return fmt.Errorf("cleanup: pending (engine target unverified; private diagnostic retained)")
}

// cleanupEngineError retains sentinel identity without appending internal detail to the public pending form.
type cleanupEngineError struct{ reason string }

// Error emits only the canonical pending form with the sanitized engine reason.
func (e *cleanupEngineError) Error() string {
	return fmt.Sprintf("cleanup: pending (engine unreachable: %s; private diagnostic retained)", e.reason)
}

// Unwrap classifies the pending error for partial-removal handling.
func (e *cleanupEngineError) Unwrap() error { return errCleanupUnreachable }

// EngineIdentity decodes the primary info query into the stable engine identity and its storage path.
// Both values stay private; consumers compare the same definition across preparation, production and cleanup.
func EngineIdentity(data []byte) (string, string, error) {
	var info struct {
		Host  struct{ Hostname string }
		Store struct{ GraphRoot string }
	}
	if json.Unmarshal(data, &info) != nil || info.Host.Hostname == "" || !strings.HasPrefix(info.Store.GraphRoot, "/") {
		return "", "", fmt.Errorf("engine storage identity unavailable or malformed")
	}
	return info.Host.Hostname + ":" + info.Store.GraphRoot, info.Store.GraphRoot, nil
}
