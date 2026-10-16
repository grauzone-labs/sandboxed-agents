// Offline process fixture for the live image harness: no network or Podman operations.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

func run(command *exec.Cmd) {
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(125)
	}
}

func main() {
	name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	if name == "ssh" {
		file, err := os.OpenFile(os.Getenv("TEST_TRANSPORT_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			panic(err)
		}
		if err := json.NewEncoder(file).Encode(os.Args[1:]); err != nil {
			panic(err)
		}
		file.Close()
		if slices.Contains(os.Args, "--fixture-streams") {
			io.Copy(os.Stdout, os.Stdin)
			fmt.Fprint(os.Stderr, "transport-stderr")
			os.Exit(23)
		}
		fmt.Print("transport-ok")
		return
	}
	if name == "podman" {
		// Model Podman's child SSH lookup, preserving remote command arguments.
		args := append([]string{"-i", "machine key with spaces", "-p", "54321", "core@127.0.0.1"}, os.Args[4:]...)
		run(exec.Command("ssh", args...))
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "forward" {
		run(exec.Command("ssh", os.Args[2:]...))
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "windows" {
		for _, query := range [][]string{{"id", "-u"}, {"cat", "/sys/fs/cgroup/cgroup.controllers"}, {"test", "-e", "/etc/wsl.conf"}} {
			args := append([]string{"machine", "ssh", "podman-machine-default"}, query...)
			out, err := exec.Command("podman", args...).CombinedOutput()
			if err != nil {
				fmt.Printf("step 2: podman %s: %v: %s", strings.Join(args, " "), err, out)
				os.Exit(1)
			}
			fmt.Print(string(out))
		}
	}
	// Use the readiness destination/port and distinguishing known_hosts option.
	cmd := exec.Command("ssh", "-v", "-F", "none", "-o", "UserKnownHostsFile=known_hosts", "-p", "2222", "agent@127.0.0.1")
	out, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 255 {
		fmt.Printf("readiness was not intentionally failed: %v %s", err, out)
		os.Exit(2)
	}
	fmt.Print(" readiness-failed-after-preflight")
}
