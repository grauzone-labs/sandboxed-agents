package cli_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// These public fixtures and SHA256 fingerprints were generated independently
// with OpenSSH ssh-keygen -t TYPE and ssh-keygen -lf FILE.pub -E sha256.
var fingerprintKeys = []struct{ path, key string }{
	{"/etc/ssh/ssh_host_ed25519_key.pub", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEaBww+ekfB4Jhc4w2QRFHxrMfLgXmRpCfrwE/b2L6m7 fixture\n"},
	{"/etc/ssh/ssh_host_ecdsa_key.pub", "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBImzPFaboqjAjpkDC1YLj2sEWxY1HPJarCYNJtQUyiLUmW0KNEXnvMNt/YEJkFyMZrm3ZHCDN+mBR+qjPq2EGek= fixture\n"},
	{"/etc/ssh/ssh_host_rsa_key.pub", "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQDIx/Ai6lYS2zP5D0cjPBNwe7VKlPyOlxZmwFuVJvQ4THodBcMuAwRNRvE9ClH5oNuur6vN2KMjPnJGsgAze/649d0Us/SfAlApzilS1KSYXrprsFGYNZvUNzzyyYcEJTILDVgfGiQ/O8phLuttNRUPAEEiptnHjoLSay1zceHg0w== fixture\n"},
}

const fingerprintOutput = "ssh-ed25519 SHA256:Uz9PmsHJGPbzd6klakDqu+UqExF7zwQK5zBZQzzFd6s\n" +
	"ecdsa-sha2-nistp256 SHA256:0MmBQckGFO78t2X4AvUiL96AyaQoB8qPx5CNGDqs/5c\n" +
	"ssh-rsa SHA256:Hh2udSs0jsTy9nRDAZ62zfc0mxPQOa7m5TKFNLNNuis\n"

func fingerprintFixture(t *testing.T) *fixture {
	t.Helper()
	f := linuxHost(t)
	configureSandboxQueries(f, "default", "")
	f.responses["container exists "+shellContainer] = response{}
	for _, key := range fingerprintKeys {
		f.responses[fingerprintRead(key.path)] = response{Output: key.key, Stderr: "harmless warning\n"}
	}
	return f
}

func fingerprintRead(path string) string {
	return "exec --user=1000:1000 --env HOME=/home/agent " + shellContainer + " cat " + path
}

// runCommand runs any command through the fake Podman seam.
func runCommand(t *testing.T, f *fixture, args ...string) (int, string, string, [][]string) {
	t.Helper()
	data, err := json.Marshal(f.responses)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_PODMAN_RESPONSES", string(data))
	var stdout, stderr bytes.Buffer
	status := cli.RunWithHost(args, &stdout, &stderr, f.host)
	return status, stdout.String(), stderr.String(), testutil.Invocations(t, f.fakes.PodmanLog)
}

// TestFingerprintPrintsPublicHostKeys proves exact ordered fingerprints and only three agent public-key reads through RunWithHost and fake Podman.
func TestFingerprintPrintsPublicHostKeys(t *testing.T) {
	f := fingerprintFixture(t)
	var expected strings.Builder
	for _, key := range fingerprintKeys {
		fields := strings.Fields(key.key)
		blob, err := base64.StdEncoding.DecodeString(fields[1])
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(blob)
		expected.WriteString(fields[0] + " SHA256:" + base64.RawStdEncoding.EncodeToString(digest[:]) + "\n")
	}
	if expected.String() != fingerprintOutput {
		t.Fatal("independent SHA256 fixture calculation differs from OpenSSH oracle")
	}
	status, stdout, stderr, calls := runCommand(t, f, "fingerprint", "agent01")
	for _, call := range calls {
		if call[0] == "exec" && (len(call) < 2 || call[1] != "--user=1000:1000") {
			t.Fatalf("public-key read must run as agent (1000:1000): %v", call)
		}
	}
	if status != 0 || stdout != fingerprintOutput || stderr != "" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
	want := sandboxQueryCalls()
	for _, key := range fingerprintKeys {
		want = append(want, strings.Fields(fingerprintRead(key.path)))
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
	if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
		t.Fatalf("unexpected SSH calls: %v", calls)
	}
}

// TestFingerprintReadFailureEmitsNothing proves a missing public key names its file without partial fingerprints through RunWithHost and fake Podman.
func TestFingerprintReadFailureEmitsNothing(t *testing.T) {
	for _, key := range fingerprintKeys {
		t.Run(key.path, func(t *testing.T) {
			f := fingerprintFixture(t)
			f.responses[fingerprintRead(key.path)] = response{Stderr: "No such file", Exit: 1}
			status, stdout, stderr, calls := runCommand(t, f, "fingerprint", "agent01")
			if status != 1 || stdout != "" || !strings.Contains(stderr, key.path) || !strings.Contains(stderr, "No such file") {
				t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
			}
			if last := calls[len(calls)-1]; !reflect.DeepEqual(last, strings.Fields(fingerprintRead(key.path))) {
				t.Fatalf("read continued after failure: %v", calls)
			}
			noMutation(t, calls)
		})
	}
}

func publicKeyText(kind string, fields ...[]byte) string {
	var wire bytes.Buffer
	for _, field := range append([][]byte{[]byte(kind)}, fields...) {
		if err := binary.Write(&wire, binary.BigEndian, uint32(len(field))); err != nil {
			panic(err)
		}
		wire.Write(field)
	}
	return kind + " " + base64.StdEncoding.EncodeToString(wire.Bytes()) + " fixture\n"
}

// TestFingerprintRejectsInvalidPublicKeys proves malformed text, wire data, and public-key material name their file without output through RunWithHost and fake Podman.
func TestFingerprintRejectsInvalidPublicKeys(t *testing.T) {
	for _, tc := range []struct {
		name  string
		index int
		key   string
	}{
		{"empty", 0, ""},
		{"invalid-base64", 1, "ecdsa-sha2-nistp256 %%%"},
		{"wrong-text-type", 0, strings.Replace(fingerprintKeys[0].key, "ssh-ed25519", "ssh-rsa", 1)},
		{"wrong-wire-type", 0, strings.Replace(publicKeyText("ssh-rsa", []byte{1, 0, 1}, []byte{5}), "ssh-rsa", "ssh-ed25519", 1)},
		{"truncated-string", 2, "ssh-rsa AAAAB3NzaA=="},
		{"type-only", 2, publicKeyText("ssh-rsa")},
		{"short-ed25519", 0, publicKeyText("ssh-ed25519", []byte{1})},
		{"extra-wire-field", 0, publicKeyText("ssh-ed25519", make([]byte, 32), []byte{1})},
		{"wrong-curve", 1, publicKeyText("ecdsa-sha2-nistp256", []byte("nistp384"), []byte{4})},
		{"invalid-point", 1, publicKeyText("ecdsa-sha2-nistp256", []byte("nistp256"), append([]byte{4}, make([]byte, 64)...))},
		{"even-exponent", 2, publicKeyText("ssh-rsa", []byte{2}, []byte{11})},
		{"zero-exponent", 2, publicKeyText("ssh-rsa", nil, []byte{11})},
		{"negative-modulus", 2, publicKeyText("ssh-rsa", []byte{3}, []byte{0x80})},
		{"zero-modulus", 2, publicKeyText("ssh-rsa", []byte{3}, nil)},
		{"even-modulus", 2, publicKeyText("ssh-rsa", []byte{3}, []byte{10})},
		{"noncanonical-mpint", 2, publicKeyText("ssh-rsa", []byte{0, 3}, []byte{11})},
		{"multiple-keys", 2, fingerprintKeys[2].key + fingerprintKeys[0].key},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fingerprintFixture(t)
			path := fingerprintKeys[tc.index].path
			f.responses[fingerprintRead(path)] = response{Output: tc.key}
			status, stdout, stderr, calls := runCommand(t, f, "fingerprint", "agent01")
			if status != 1 || stdout != "" || !strings.Contains(stderr, path) || !strings.Contains(stderr, "invalid public key") {
				t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
			}
			if len(calls) != 7+tc.index {
				t.Fatalf("read continued after invalid key: %v", calls)
			}
		})
	}
}

// TestFingerprintRequiresName proves missing NAME returns usage without any process through RunWithHost and fake Podman.
func TestFingerprintRequiresName(t *testing.T) {
	f := linuxHost(t)
	status, stdout, stderr, calls := runCommand(t, f, "fingerprint")
	want := "step 1: sandbox name must follow the command path\nusage: sandboxed-agents COMMAND [ARG...]\n"
	if status != 2 || stdout != "" || stderr != want || len(calls) != 0 {
		t.Fatalf("status=%d stdout=%q stderr=%q calls=%v", status, stdout, stderr, calls)
	}
	if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
		t.Fatalf("unexpected SSH calls: %v", calls)
	}
}

// TestFingerprintRefusesUnavailableSandbox proves unknown or stopped sandboxes cannot read host keys and stopped errors suggest start NAME through RunWithHost and fake Podman.
func TestFingerprintRefusesUnavailableSandbox(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		value     response
		message   string
		count     int
	}{
		{"unknown", "container exists " + shellContainer, response{Exit: 1}, "sandbox \"agent01\" does not exist in this controller group\n", 5},
		{"stopped", shellInspect, response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default"}`, "false")}, "sandbox \"agent01\" is stopped; run sandboxed-agents start agent01\n", 6},
		{"unverified", shellInspect, response{Output: "unknown\n"}, "read container " + shellContainer + ": invalid inspect answer\n", 2},
		{"missing-running-state", shellInspect, response{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}},"State":{}}]`}, "read container " + shellContainer + ": running state is missing\n", 6},
		{"invalid-running-type", shellInspect, response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default"}`, `"unknown"`)}, "read container " + shellContainer + ": invalid inspect answer\n", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fingerprintFixture(t)
			f.responses[tc.key] = tc.value
			status, stdout, stderr, calls := runCommand(t, f, "fingerprint", "agent01")
			if status != 1 || stdout != "" || stderr != tc.message || len(calls) != tc.count {
				t.Fatalf("status=%d stdout=%q stderr=%q calls=%v", status, stdout, stderr, calls)
			}
			noMutation(t, calls)
		})
	}
}

// TestFingerprintSkipsLinuxPreflight proves fingerprints are read with only Podman on PATH and no prerequisite queries through RunWithHost and fake Podman.
func TestFingerprintSkipsLinuxPreflight(t *testing.T) {
	f := fingerprintFixture(t)
	f.host.ReadFile = func(path string) ([]byte, error) {
		t.Fatalf("unexpected prerequisite read: %s", path)
		return nil, os.ErrNotExist
	}
	f.host.CurrentUser = func() (*user.User, error) { t.Fatal("unexpected prerequisite user query"); return nil, os.ErrNotExist }
	podmanOnly := t.TempDir()
	data, err := os.ReadFile(fakeBinary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(podmanOnly, filepath.Base(fakePath(f, "podman"))), data, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", podmanOnly)
	status, stdout, stderr, calls := runCommand(t, f, "fingerprint", "agent01")
	want := sandboxQueryCalls()
	for _, key := range fingerprintKeys {
		want = append(want, strings.Fields(fingerprintRead(key.path)))
	}
	if status != 0 || stdout != fingerprintOutput || stderr != "" || !reflect.DeepEqual(calls, want) {
		t.Fatalf("status=%d stdout=%q stderr=%q calls=%v want=%v", status, stdout, stderr, calls, want)
	}
}

// TestWindowsFingerprintUsesSelectedTarget proves all agent host-key reads retain the selected connection and remove remote environment overrides through RunWithHost and fake Podman.
func TestWindowsFingerprintUsesSelectedTarget(t *testing.T) {
	f := windowsHost(t)
	configureSandboxQueries(f, "default", "--connection chosen ")
	envLog := logAmbientPodmanEnvironment(t)
	f.responses["--connection chosen container exists "+shellContainer] = response{}
	for _, key := range fingerprintKeys {
		f.responses["--connection chosen "+fingerprintRead(key.path)] = response{Output: key.key}
	}
	status, stdout, stderr, calls := runCommand(t, f, "fingerprint", "agent01")
	if status != 0 || stdout != fingerprintOutput || stderr != "" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
	wantCalls := [][]string{{"machine", "list", "--format", "json"}, {"machine", "inspect", "chosen"}}
	for _, call := range sandboxQueryCalls() {
		wantCalls = append(wantCalls, append([]string{"--connection", "chosen"}, call...))
	}
	for _, key := range fingerprintKeys {
		wantCalls = append(wantCalls, append([]string{"--connection", "chosen"}, strings.Fields(fingerprintRead(key.path))...))
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls=%v want=%v", calls, wantCalls)
	}

	for i, key := range fingerprintKeys {
		want := append([]string{"--connection", "chosen"}, strings.Fields(fingerprintRead(key.path))...)
		if !reflect.DeepEqual(calls[len(calls)-3+i], want) {
			t.Fatalf("key read=%v want=%v", calls[len(calls)-3+i], want)
		}
	}
	assertNoAmbientPodmanEnvironment(t, envLog)
}

type hostFileSnapshot struct {
	Mode fs.FileMode
	Data string
}

func snapshotHostFiles(t *testing.T, root string) map[string]hostFileSnapshot {
	t.Helper()
	snapshot := make(map[string]hostFileSnapshot)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var data []byte
		if !entry.IsDir() {
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		snapshot[name] = hostFileSnapshot{info.Mode(), string(data)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// TestFingerprintLeavesHostFilesUntouched proves success and failures never read host state or alter its names, modes or bytes, including SSH setup, through RunWithHost and temporary files.
func TestFingerprintLeavesHostFilesUntouched(t *testing.T) {
	for _, outcome := range []string{"success", "missing", "invalid", "unknown", "stopped", "query-failure", "skipped-preflight"} {
		t.Run(outcome, func(t *testing.T) {
			f := fingerprintFixture(t)
			root := t.TempDir()
			home, state := filepath.Join(root, "home"), filepath.Join(root, "state")
			sshDir := filepath.Join(home, ".ssh")
			group := filepath.Join(state, "sandboxed-agents", "group-default")
			for _, dir := range []string{sshDir, group} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, file := range []struct {
				path, data string
				mode       fs.FileMode
			}{
				{filepath.Join(sshDir, "config"), "Host preserved\n  HostName example\n", 0640},
				{filepath.Join(sshDir, "known_hosts"), "preserved key\n", 0600},
				{filepath.Join(sshDir, "id_ed25519"), "private sentinel\x00\xff", 0600},
				{filepath.Join(group, "existing.lock"), "", 0600},
				{filepath.Join(group, "existing.conf"), "state sentinel\x00\xff", 0640},
			} {
				if err := os.WriteFile(file.path, []byte(file.data), file.mode); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("XDG_STATE_HOME", state)
			t.Setenv("LOCALAPPDATA", state)
			before := snapshotHostFiles(t, root)
			readFile := f.host.ReadFile
			f.host.ReadFile = func(path string) ([]byte, error) {
				if strings.HasPrefix(path, root) {
					t.Fatalf("fingerprint read host SSH/state: %s", path)
				}
				return readFile(path)
			}
			wantStatus := 1
			switch outcome {
			case "success":
				wantStatus = 0
			case "skipped-preflight":
				wantStatus = 0
				f.responses["info --format json"] = response{Exit: 125}
			case "missing":
				f.responses[fingerprintRead(fingerprintKeys[2].path)] = response{Exit: 1}
			case "invalid":
				f.responses[fingerprintRead(fingerprintKeys[2].path)] = response{Output: "invalid"}
			case "unknown":
				f.responses["container exists "+shellContainer] = response{Exit: 1}
			case "stopped":
				f.responses[shellInspect] = response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default"}`, "false")}
			case "query-failure":
				f.responses["container exists "+shellContainer] = response{Exit: 125}
			}
			status, stdout, stderr, calls := runCommand(t, f, "fingerprint", "agent01")
			if status != wantStatus {
				t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
			}
			if after := snapshotHostFiles(t, root); !reflect.DeepEqual(after, before) {
				t.Fatalf("host files changed: before=%v after=%v", before, after)
			}
			if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
				t.Fatalf("unexpected SSH calls: %v", calls)
			}
			noMutation(t, calls)
		})
	}
}
