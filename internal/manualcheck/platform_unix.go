//go:build !windows

package manualcheck

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// childEcho builds the child process that proves the controller group is exported: a POSIX shell
// printing the variable.
func childEcho(group string) *exec.Cmd {
	command := exec.Command("sh", "-c", `echo "group: $SANDBOXED_AGENTS_GROUP"`)
	command.Env = append(os.Environ(), "SANDBOXED_AGENTS_GROUP="+group)
	return command
}

// freeBytes returns the free space of the filesystem that holds path.
func freeBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

// processIdentity returns the start ticks and owner of a process from /proc, for the evidence of
// which process was killed; the kill itself goes through the process handle Go holds.
func processIdentity(pid int) string {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "unavailable"
	}
	fields := strings.Fields(string(stat))
	start := "unknown"
	if len(fields) > 21 {
		start = fields[21]
	}
	info, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	owner := "unknown"
	if err == nil {
		if sys, ok := info.Sys().(*syscall.Stat_t); ok {
			owner = strconv.Itoa(int(sys.Uid))
		}
	}
	return "start_ticks=" + start + " uid=" + owner
}

// hostDiagnostics returns platform-specific, secret-free host facts for the preflight.
func hostDiagnostics() []*exec.Cmd {
	user := os.Getenv("USER")
	return []*exec.Cmd{
		exec.Command("uname", "-sr"),
		exec.Command("uname", "-m"),
		exec.Command("id", "-u"),
		exec.Command("sh", "-c", `printf 'subuid=%s subgid=%s\n' "$(grep -c "^$1:" /etc/subuid 2>/dev/null)" "$(grep -c "^$1:" /etc/subgid 2>/dev/null)"`, "sh", user),
		exec.Command("sh", "-c", `test -c /dev/net/tun && echo 'tun present' || echo 'tun absent'`),
		exec.Command("sh", "-c", `command -v loginctl >/dev/null && loginctl show-user "$1" -p Linger --value || echo 'loginctl absent'`, "sh", user),
	}
}

// nullInput is the file read as "no standard input".
const nullInput = os.DevNull
