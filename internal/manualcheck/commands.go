package manualcheck

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// SA builds a command of the verified executable with the run's controller group exported.
func (r *Run) SA(args ...string) *exec.Cmd {
	command := exec.Command(r.Executable, args...)
	command.Env = append(os.Environ(), "SANDBOXED_AGENTS_GROUP="+r.Group)
	return command
}

// Podman builds a direct Podman call; on Windows it names the machine the executable works on.
func (r *Run) Podman(args ...string) *exec.Cmd {
	if r.PodmanConnection != "" {
		args = append([]string{"--connection", r.PodmanConnection}, args...)
	}
	return exec.Command("podman", args...)
}

// PodmanOutput runs a Podman query and returns its trimmed standard output.
func (r *Run) PodmanOutput(args ...string) (string, error) {
	output, err := r.Podman(args...).Output()
	if err != nil {
		return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output)), nil
}

// ShellStep runs a script as `agent` inside a sandbox through `shell NAME`, which reads commands
// from standard input when no terminal is attached, and records it as a step.
func (r *Run) ShellStep(dir, name, sandbox, script string) (StepResult, error) {
	return Step(dir, name, strings.NewReader(script+"\n"), r.SA("shell", sandbox))
}

// Container returns the container name of a sandbox of the run's group.
func (r *Run) Container(sandbox string) string {
	return "sandboxed-agents." + r.Group + "." + sandbox
}

// BackupContainer returns the backup container name of a sandbox of the run's group.
func (r *Run) BackupContainer(sandbox string) string {
	return "sandboxed-agents-backup." + r.Group + "." + sandbox
}

// ContainerExists reports whether a container of that name exists on the engine.
func (r *Run) ContainerExists(name string) bool {
	return r.Podman("container", "exists", name).Run() == nil
}

// VerifyExecutable checks that an executable prints the version line and asset hash of the
// preview under test and returns the executable's SHA-256.
func VerifyExecutable(executable, version, asset string) (string, error) {
	output, err := exec.Command(executable, "version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("version failed: %v: %s", err, output)
	}
	lines := strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n")
	hasLine := func(text string) bool {
		for _, line := range lines {
			if line == text {
				return true
			}
		}
		return false
	}
	if !hasLine("sandboxed-agents " + version) {
		return "", fmt.Errorf("version mismatch: wanted %q in: %s", "sandboxed-agents "+version, output)
	}
	if !hasLine("asset-sha256 " + asset) {
		return "", fmt.Errorf("asset-sha256 mismatch: wanted %s in: %s", asset, output)
	}
	return FileSHA256(executable)
}

// FileSHA256 returns the lowercase hexadecimal SHA-256 of a file.
func FileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// DefaultPlatform returns the record platform of the host the runner runs on.
func DefaultPlatform() string {
	if runtime.GOOS == "windows" {
		return PlatformWindows
	}
	return PlatformLinux
}

// GroupProof proves that the controller group reaches a child process and that `list` shows only
// sandboxes of this checklist, and writes the proof to path.
func (r *Run) GroupProof(path string) error {
	child := childEcho(r.Group)
	output, err := child.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "group: "+r.Group {
		return fmt.Errorf("the controller group does not reach a child process: %q (%v)", strings.TrimSpace(string(output)), err)
	}
	list, err := r.SA("list").CombinedOutput()
	if err != nil {
		return fmt.Errorf("list failed: %v: %s", err, list)
	}
	text := fmt.Sprintf("utc=%s\nchild=%s\nlist_begin\n%slist_end\n", utcNow(), strings.TrimSpace(string(output)), list)
	if err := writePrivate(path, text); err != nil {
		return err
	}
	for _, name := range ListNames(string(list)) {
		if !containsString(KnownSandboxes, name) {
			return fmt.Errorf("list shows a sandbox this checklist did not create: %s", name)
		}
	}
	return nil
}

// containsString reports whether values holds value.
func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
