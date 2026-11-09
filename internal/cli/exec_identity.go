package cli

// appendRootIdentity explicitly selects container root for administrative execs (ADR-0006).
// Callers retain command construction and all execution policy.
func appendRootIdentity(args []string) []string {
	return append(args, "--user=0:0")
}

// appendAgentIdentity selects agent's UID/GID and home before any agent work (ADR-0006).
// Callers retain option order, stdin, TTY, workdir, command, and execution policy.
func appendAgentIdentity(args []string) []string {
	return append(args, "--user=1000:1000", "--env", "HOME=/home/agent")
}
