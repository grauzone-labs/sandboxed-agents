package cli_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

const portInventory = "ps --all --format json"

// TestMalformedPortsFailBeforeBrokenPreflight proves invalid explicit ports report usage errors without Podman or SSH calls through RunWithHost and process fakes.
func TestMalformedPortsFailBeforeBrokenPreflight(t *testing.T) {
	for _, value := range []string{"abc", "0", "70000"} {
		t.Run(value, func(t *testing.T) {
			f := linuxHost(t)
			f.responses["--version"] = response{Output: "broken preflight", Exit: 125}
			f.responses["info --format json"] = response{Output: "broken preflight", Exit: 125}
			status, output, calls := f.run(t, "up", "demo", "--port", value)
			if status != 2 || !strings.Contains(output, "--port:") {
				t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
			}
			if len(calls) != 0 {
				t.Fatalf("Podman reached before port validation: %v", calls)
			}
			if sshCalls := testutil.Invocations(t, f.fakes.SSHLog); len(sshCalls) != 0 {
				t.Fatalf("SSH reached before port validation: %v", sshCalls)
			}
		})
	}
}

func portHost(t *testing.T) *fixture {
	t.Helper()
	f := linuxHost(t)
	scriptNewSandbox(t, f, "default", "demo")
	f.responses["container exists sandboxed-agents.default.demo"] = response{Exit: 1}
	f.responses["image exists "+baseTag(t)] = response{}
	f.responses[portInventory] = response{Output: "[]"}
	return f
}

// TestUpAllocatesLoopbackPort proves up creates only a loopback SSH publication through RunWithHost and fake Podman with the required owned resources.
func TestUpAllocatesLoopbackPort(t *testing.T) {
	f := portHost(t)
	status, output, calls := f.run(t, "up", "demo")
	if status != 0 || !strings.Contains(output, "--publish 127.0.0.1:2222:22/tcp") {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	assertNewSandbox(t, calls, "", "default", "demo", baseTag(t), "", "2222", "8589934592", "4", "2048", "1073741824")
	expected := [][]string{strings.Fields(portInventory), {"image", "exists", baseTag(t)}}
	if !reflect.DeepEqual(calls[7:9], expected) {
		t.Fatalf("port/image query order=%v; want %v", calls, expected)
	}
}

// TestUpAllocatesDistinctPortsInSameGroup proves successive sandbox creations skip ports recorded by their controller group through RunWithHost and fake Podman.
func TestUpAllocatesDistinctPortsInSameGroup(t *testing.T) {
	f := portHost(t)
	status, first, firstCalls := f.run(t, "up", "demo")
	if status != 0 || !strings.Contains(first, "127.0.0.1:2222:22/tcp") {
		t.Fatalf("first=%q", first)
	}
	assertNewSandbox(t, firstCalls, "", "default", "demo", baseTag(t), "", "2222", "8589934592", "4", "2048", "1073741824")
	f.responses[portInventory] = response{Output: `[{"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.port":"2222"}}]`}
	scriptNewSandbox(t, f, "default", "second")
	f.responses["container exists sandboxed-agents.default.second"] = response{Exit: 1}
	status, second, calls := f.run(t, "up", "second")
	if status != 0 || !strings.Contains(second, "127.0.0.1:2223:22/tcp") {
		t.Fatalf("second=%q", second)
	}
	assertNewSandbox(t, calls[len(firstCalls):], "", "default", "second", baseTag(t), "", "2223", "8589934592", "4", "2048", "1073741824")
}

// TestUpUsesRequestedLoopbackPort proves an explicit SSH port uses only the requested loopback publication through RunWithHost.
func TestUpUsesRequestedLoopbackPort(t *testing.T) {
	f := portHost(t)
	status, output, calls := f.run(t, "up", "demo", "--port", "2300")
	if status != 0 || !strings.Contains(output, "--publish 127.0.0.1:2300:22/tcp") {
		t.Fatalf("status=%d output=%q", status, output)
	}
	assertNewSandbox(t, calls, "", "default", "demo", baseTag(t), "", "2300", "8589934592", "4", "2048", "1073741824")
}

// TestUpSkipsStoppedPortsAcrossGroups proves all groups' recorded SSH ports remain reserved for stopped sandboxes and no foreign group is queried or mutated beyond the port inventory through RunWithHost.
func TestUpSkipsStoppedPortsAcrossGroups(t *testing.T) {
	f := portHost(t)
	t.Setenv("SANDBOXED_AGENTS_GROUP", "current")
	scriptNewSandbox(t, f, "current", "demo")
	f.responses["container exists sandboxed-agents.current.demo"] = response{Exit: 1}
	f.responses[portInventory] = response{Output: `[{"State":"exited","Labels":{"io.sandboxed-agents.owner":"other","io.sandboxed-agents.port":"2222"}},{"State":"running","Labels":{"io.sandboxed-agents.owner":"third","io.sandboxed-agents.port":"2223"}}]`}
	status, output, calls := f.run(t, "up", "demo")
	if status != 0 || !strings.Contains(output, "127.0.0.1:2224:22/tcp") {
		t.Fatalf("status=%d output=%q", status, output)
	}
	assertNewSandbox(t, calls, "", "current", "demo", baseTag(t), "", "2224", "8589934592", "4", "2048", "1073741824")
	for _, call := range calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "other") || strings.Contains(joined, "third") || (len(call) > 0 && call[0] == "inspect") {
			t.Fatalf("foreign group accessed beyond port inventory: %v", call)
		}
	}
}

// TestUpRefusesRecordedRequestedPort proves an explicit port recorded by another stopped sandbox fails without changing either group through RunWithHost.
func TestUpRefusesRecordedRequestedPort(t *testing.T) {
	f := portHost(t)
	f.responses[portInventory] = response{Output: `[{"State":"exited","Labels":{"io.sandboxed-agents.owner":"foreign","io.sandboxed-agents.port":"2300"}}]`}
	status, output, calls := f.run(t, "up", "demo", "--port", "2300")
	if status != 1 || !strings.Contains(output, "SSH port 2300 is already recorded") {
		t.Fatalf("status=%d output=%q", status, output)
	}
	noMutation(t, calls)
}

// TestUpRefusesUnreadableRecordedPorts proves malformed or failed port inventories cannot yield a publication through RunWithHost and fake Podman.
func TestUpRefusesUnreadableRecordedPorts(t *testing.T) {
	for _, value := range []response{{Output: "{"}, {Exit: 125}, {Output: `[{"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.port":"invalid"}}]`}} {
		t.Run(value.Output, func(t *testing.T) {
			f := portHost(t)
			f.responses[portInventory] = value
			status, output, calls := f.run(t, "up", "demo")
			if status != 1 || strings.Contains(output, "--publish") {
				t.Fatalf("status=%d output=%q", status, output)
			}
			noMutation(t, calls)
		})
	}
}

// TestUpSkipsUnavailableHostPorts proves automatic port allocation skips an occupied loopback socket through RunWithHost's host probe seam.
func TestUpSkipsUnavailableHostPorts(t *testing.T) {
	f := portHost(t)
	var probed []uint16
	f.host.PortAvailable = func(port uint16) error {
		probed = append(probed, port)
		if port == 2222 {
			return fmt.Errorf("address in use")
		}
		return nil
	}
	status, output, calls := f.run(t, "up", "demo")
	if status != 0 || !strings.Contains(output, "127.0.0.1:2223:22/tcp") || !reflect.DeepEqual(probed, []uint16{2222, 2223}) {
		t.Fatalf("status=%d output=%q probes=%v", status, output, probed)
	}
	assertNewSandbox(t, calls, "", "default", "demo", baseTag(t), "", "2223", "8589934592", "4", "2048", "1073741824")
}

// TestUpRefusesMissingRecordedPort proves an incomplete managed sandbox port record cannot be treated as an available port through RunWithHost.
func TestUpRefusesMissingRecordedPort(t *testing.T) {
	for _, inventory := range []string{`null`, `[{"Labels":{"io.sandboxed-agents.owner":"foreign"}}]`} {
		t.Run(inventory, func(t *testing.T) {
			f := portHost(t)
			f.responses[portInventory] = response{Output: inventory}
			status, output, calls := f.run(t, "up", "demo")
			if status != 1 || strings.Contains(output, "--publish") {
				t.Fatalf("status=%d output=%q", status, output)
			}
			noMutation(t, calls)
		})
	}
}

// TestUpRefusesUnavailableRequestedPort proves an occupied explicit loopback port fails without choosing a substitute through RunWithHost's host probe seam.
func TestUpRefusesUnavailableRequestedPort(t *testing.T) {
	f := portHost(t)
	f.host.PortAvailable = func(uint16) error { return fmt.Errorf("address in use") }
	status, output, calls := f.run(t, "up", "demo", "--port", "2300")
	if status != 1 || !strings.Contains(output, "SSH port 2300 is unavailable on 127.0.0.1") || strings.Contains(output, "--publish") {
		t.Fatalf("status=%d output=%q", status, output)
	}
	noMutation(t, calls)
}

// TestUpSkipsStoppedNamedSandboxPortWithoutOwner proves a stopped sandbox's recorded port stays reserved even when its owner label is missing through RunWithHost and fake Podman.
func TestUpSkipsStoppedNamedSandboxPortWithoutOwner(t *testing.T) {
	f := portHost(t)
	f.responses[portInventory] = response{Output: `[{"Names":["sandboxed-agents.other.x"],"State":"exited","Labels":{"io.sandboxed-agents.port":"2222"}}]`}
	status, output, calls := f.run(t, "up", "demo")
	if status != 0 || !strings.Contains(output, "127.0.0.1:2223:22/tcp") {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	assertNewSandbox(t, calls, "", "default", "demo", baseTag(t), "", "2223", "8589934592", "4", "2048", "1073741824")
}

// TestUpRefusesRecordedPortBeforeMissingImageBuild proves a taken explicit port stops up before building an absent image through RunWithHost and fake Podman.
func TestUpRefusesRecordedPortBeforeMissingImageBuild(t *testing.T) {
	f := portHost(t)
	f.responses["image exists "+baseTag(t)] = response{Exit: 1}
	f.responses["build *"] = response{}
	f.responses[portInventory] = response{Output: `[{"Labels":{"io.sandboxed-agents.owner":"other","io.sandboxed-agents.port":"2300"}}]`}
	status, output, calls := f.run(t, "up", "demo", "--port", "2300")
	if status != 1 || !strings.Contains(output, "SSH port 2300 is already recorded") {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	noMutation(t, calls)
	expected := [][]string{{"--version"}, {"info", "--format", "json"}, {"container", "exists", "sandboxed-agents.default.demo"}, {"volume", "exists", "sandboxed-agents.default.demo.workspace"}, {"volume", "exists", "sandboxed-agents.default.demo.home"}, {"volume", "exists", "sandboxed-agents.default.demo.ssh"}, {"container", "exists", "sandboxed-agents-backup.default.demo"}, strings.Fields(portInventory)}
	if !reflect.DeepEqual(calls, expected) {
		t.Fatalf("taken port queries=%v; want %v", calls, expected)
	}
}
