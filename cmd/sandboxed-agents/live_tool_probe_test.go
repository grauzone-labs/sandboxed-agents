package main_test

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// TestLiveToolProbeRejectsExposedIPv6Listener proves an IPv6 wildcard listener makes the actual Tool probe fail at its filesystem and HTTP boundaries, without a sandbox or network access.
func TestLiveToolProbeRejectsExposedIPv6Listener(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is needed to execute the JavaScript probe offline")
	}
	command := exec.Command(node, "testdata/live_t3code_probe_harness.js", "ipv6-any")
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "IPv6 service is not bound to loopback") {
		t.Fatalf("exposed IPv6 probe: %v %s; want a loopback failure", err, output)
	}
}

// TestLiveToolProbeReportsObservedIdentity proves the actual JavaScript probe reports the service's parsed identity with IPv4-only, loopback IPv6, and unavailable IPv6 tables through supplied filesystem and HTTP boundaries.
func TestLiveToolProbeReportsObservedIdentity(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is needed to execute the JavaScript probe offline")
	}
	for _, scenario := range []string{"ipv4-only", "ipv6-loopback", "ipv6-unavailable", "unrelated-ipv6", "connected-ipv6"} {
		t.Run(scenario, func(t *testing.T) {
			output, err := exec.Command(node, "testdata/live_t3code_probe_harness.js", scenario).CombinedOutput()
			if err != nil {
				t.Fatalf("probe %s: %v %s", scenario, err, output)
			}
			var observation liveToolObservation
			if err := json.Unmarshal(output, &observation); err != nil {
				t.Fatalf("read observed service identity: %v %s", err, output)
			}
			if observation.UID != 1000 || observation.GID != 1000 || observation.ServicePID != 201 || observation.HTTPStatus != 200 {
				t.Fatalf("unexpected service observation: %+v", observation)
			}
		})
	}
}

// TestLiveToolProbeRejectsNonloopbackIPv6 proves a nonloopback IPv6 listener makes the actual probe fail through supplied filesystem and HTTP boundaries.
func TestLiveToolProbeRejectsNonloopbackIPv6(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is needed to execute the JavaScript probe offline")
	}
	output, err := exec.Command(node, "testdata/live_t3code_probe_harness.js", "ipv6-nonloopback").CombinedOutput()
	if err == nil || !strings.Contains(string(output), "IPv6 service is not bound to loopback") {
		t.Fatalf("nonloopback IPv6 probe: %v %s; want a loopback failure", err, output)
	}
}

// TestLiveToolProbeRejectsProcessIdentityMismatch proves every real, effective, saved, and filesystem UID/GID of the supervisor, service, and descendants must be 1000 through the actual probe's supplied filesystem and HTTP boundaries.
func TestLiveToolProbeRejectsProcessIdentityMismatch(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is needed to execute the JavaScript probe offline")
	}
	for _, processKind := range []string{"supervisor", "service", "child"} {
		for _, field := range []string{"uid", "gid"} {
			for index := range 4 {
				scenario := fmt.Sprintf("bad-identity-%s-%s-%d", processKind, field, index)
				t.Run(scenario, func(t *testing.T) {
					output, err := exec.Command(node, "testdata/live_t3code_probe_harness.js", scenario).CombinedOutput()
					if err == nil || !strings.Contains(string(output), "Tool "+field+" differs from 1000") {
						t.Fatalf("mismatched identity probe: %v %s; want a %s failure", err, output, field)
					}
				})
			}
		}
	}
}
