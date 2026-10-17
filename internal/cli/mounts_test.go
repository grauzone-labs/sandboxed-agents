package cli_test

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
)

// mountBoundaryError checks recorded Podman arguments against the named-volume and single-
// workspace-bind policy.
func mountBoundaryError(calls [][]string, prefix, workspace string) error {
	canonicalWorkspace := ""
	if workspace != "" {
		var err error
		canonicalWorkspace, err = canonicalMountDirectory(workspace)
		if err != nil {
			return fmt.Errorf("invalid expected workspace %q: %w", workspace, err)
		}
	}
	allowedVolumes := map[string]string{prefix + ".workspace": "/workspace", prefix + ".home": "/home/agent", prefix + ".ssh": "/etc/ssh"}
	for _, call := range calls {
		for index := 0; index < len(call); index++ {
			arg := call[index]
			if strings.HasPrefix(arg, "--volume") || strings.HasPrefix(arg, "-v") || strings.HasPrefix(arg, "--device") {
				return fmt.Errorf("unexpected alternate mount argument %q in %v", arg, call)
			}
			value := ""
			if arg == "--mount" {
				index++
				if index >= len(call) {
					return fmt.Errorf("missing mount value in %v", call)
				}
				value = call[index]
			} else if strings.HasPrefix(arg, "--mount=") {
				value = strings.TrimPrefix(arg, "--mount=")
			} else {
				continue
			}
			records, err := csv.NewReader(strings.NewReader(value)).ReadAll()
			if err != nil || len(records) != 1 || len(records[0]) != 3 {
				return fmt.Errorf("invalid mount %q in %v", value, call)
			}
			fields := map[string]string{}
			for _, field := range records[0] {
				key, val, ok := strings.Cut(field, "=")
				if !ok || fields[key] != "" {
					return fmt.Errorf("duplicate/malformed mount field %q", field)
				}
				fields[key] = val
			}
			switch fields["type"] {
			case "bind":
				if workspace == "" || fields["destination"] != "/workspace" {
					return fmt.Errorf("unexpected host/socket mount %q in %v", value, call)
				}
				canonicalSource, err := canonicalMountDirectory(fields["source"])
				if err != nil || canonicalSource != canonicalWorkspace {
					return fmt.Errorf("unexpected host/socket mount %q in %v", value, call)
				}
			case "volume":
				destination, ok := allowedVolumes[fields["source"]]
				if !ok || fields["destination"] != destination || workspace != "" && destination == "/workspace" {
					return fmt.Errorf("unexpected named volume %q in %v", value, call)
				}
			default:
				return fmt.Errorf("unexpected mount type in %q", value)
			}
		}
	}
	return nil
}

// canonicalMountDirectory resolves a workspace through the public parser's directory and
// protected-path checks.
func canonicalMountDirectory(path string) (string, error) {
	// Parse uses the product's directory and protected-path checks and native
	// resolver, including Windows junction and short-name canonicalization.
	request, err := cli.Parse([]string{"up", "agent01", path}, "default")
	if err != nil {
		return "", err
	}
	return request.Args[1], nil
}

// TestMountAuditRejectsHiddenHostMounts proves the test boundary guard rejects host/socket mounts anywhere in query or build argv, including connection prefixes and alternate mount flags.
func TestMountAuditRejectsHiddenHostMounts(t *testing.T) {
	workspace := t.TempDir()
	for _, call := range [][]string{
		{"--connection", "chosen", "info", "--mount", "type=bind,source=/home/host,destination=/home/agent"},
		{"build", "--mount=type=bind,source=/run/ssh-agent.sock,destination=/workspace"},
		{"info", "-v", "/var/run/docker.sock:/var/run/docker.sock"},
		{"build", "--volume=/tmp/.X11-unix:/tmp/.X11-unix"},
		{"info", "-v/run/podman/podman.sock:/socket"},
		{"info", "--volumes-from", "foreign"},
		{"info", "--device", "/dev/fuse"},
	} {
		t.Run(strings.Join(call, " "), func(t *testing.T) {
			if err := mountBoundaryError([][]string{call}, "sandboxed-agents.default.agent01", workspace); err == nil {
				t.Fatalf("mount audit accepted forbidden mount in %v", call)
			}
		})
	}
}

// TestMountAuditComparesCanonicalDirectories proves different spellings of one workspace pass while a distinct directory or non-directory path fails through the mount audit and public Parse resolver seam.
func TestMountAuditComparesCanonicalDirectories(t *testing.T) {
	expected := t.TempDir()
	alias := filepath.Join(t.TempDir(), "workspace-alias")
	if err := os.Symlink(expected, alias); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, alias)
	if err != nil {
		t.Fatal(err)
	}
	distinct := t.TempDir()
	file := filepath.Join(t.TempDir(), "agent.sock")
	if err := os.WriteFile(file, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, expected, source string
		accepted               bool
	}{
		{"resolved expected and relative alias source", expected, relative, true},
		{"alias expected and resolved source", alias, expected, true},
		{"distinct directory", expected, distinct, false},
		{"socket path", expected, file, false},
		{"socket on both sides", file, file, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := [][]string{{"--connection", "chosen", "create", "--mount", "type=bind,source=" + tc.source + ",destination=/workspace"}}
			err := mountBoundaryError(calls, "sandboxed-agents.default.agent01", tc.expected)
			if (err == nil) != tc.accepted {
				t.Fatalf("mount audit error=%v; expected acceptance=%t for source=%q workspace=%q", err, tc.accepted, tc.source, tc.expected)
			}
		})
	}
}
