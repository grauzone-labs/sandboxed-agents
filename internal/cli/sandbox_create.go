package cli

import (
	"fmt"
	"strconv"
)

// sandboxCreateArgs keeps the isolation, identity, and storage policy shared by up and update.
func sandboxCreateArgs(request Request, port uint16, workspaceMount, tag string) []string {
	names := DerivedNames(request.Group, request.Name)
	labels := []string{"--label", OwnerLabel + "=" + request.Group, "--label", SandboxNameLabel + "=" + request.Name}
	args := append([]string{"create", "--name", names.Container}, labels...)
	args = append(args, "--label", SSHPortLabel+"="+strconv.Itoa(int(port)),
		"--userns=keep-id:uid=1000,gid=1000", "--user=0:0", "--security-opt=no-new-privileges", "--network=pasta:--no-map-gw",
		"--memory", strconv.FormatInt(request.Resources.Memory, 10), "--cpus", strconv.FormatFloat(request.Resources.CPUs, 'f', -1, 64),
		"--pids-limit", strconv.FormatInt(request.Resources.PIDsLimit, 10), "--shm-size", strconv.FormatInt(request.Resources.ShmSize, 10),
		"--publish", fmt.Sprintf("127.0.0.1:%d:22/tcp", port),
		"--label", ToolchainsLabel+"="+toolchainSet(request.Toolchains),
		"--mount", workspaceMount,
		"--mount", "type=volume,source="+names.Home+",destination=/home/agent",
		"--mount", "type=volume,source="+names.SSH+",destination=/etc/ssh", tag)
	return args
}
