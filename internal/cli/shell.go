package cli

import "io"

// openShell opens Bash in a running sandbox as the agent user and forwards the attached process
// status.
func openShell(request Request, p podmanTarget, interactive bool, stdout, stderr io.Writer) (int, error) {
	objects, err := requireSandbox(request, p)
	if err != nil {
		return 1, err
	}
	names, err := requireRunning(request, objects)
	if err != nil {
		return 1, err
	}
	args := []string{"exec", "-i"}
	if interactive {
		args = append(args, "-t")
	}
	args = append(args, "--user=1000:1000", "--env", "HOME=/home/agent", "--workdir=/workspace", names.Container, "/bin/bash")
	return p.run(p.host.Stdin, stdout, stderr, args...)
}
