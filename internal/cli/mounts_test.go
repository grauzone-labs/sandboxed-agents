package cli_test

import (
	"encoding/csv"
	"fmt"
	"strings"
	"testing"
)

func mountBoundaryError(calls [][]string, prefix, workspace string) error {
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
				if workspace == "" || fields["source"] != workspace || fields["destination"] != "/workspace" {
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

// TestMountAuditRejectsHiddenHostMounts proves the test boundary guard rejects host/socket mounts anywhere in query or build argv, including connection prefixes and alternate mount flags.
func TestMountAuditRejectsHiddenHostMounts(t *testing.T) {
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
			if err := mountBoundaryError([][]string{call}, "sandboxed-agents.default.agent01", "/project"); err == nil {
				t.Fatalf("mount audit accepted forbidden mount in %v", call)
			}
		})
	}
}
