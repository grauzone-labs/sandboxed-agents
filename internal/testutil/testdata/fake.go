// This executable is built by the test harness, never shipped with the CLI.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

func main() {
	// A bounded child deliberately keeps the parent's inherited output open.
	if len(os.Args) == 3 && os.Args[1] == "--test-hold-output" {
		defer os.WriteFile(os.Args[2]+".done", nil, 0600)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(os.Args[2]); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		return
	}

	prefix := "TEST_" + strings.ReplaceAll(strings.ToUpper(strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")), "-", "_")
	file, err := os.OpenFile(os.Getenv(prefix+"_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(125)
	}
	err = json.NewEncoder(file).Encode(os.Args[1:])
	file.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(125)
	}
	// Live rollback injection must not intercept Podman's own machine transport.
	if prefix == "TEST_SSH" && os.Getenv("TEST_SSH_REAL_EXECUTABLE") != "" {
		args := os.Args[1:]
		port := slices.Index(args, "-p")
		readiness := port >= 0 && port+1 < len(args) && args[port+1] == os.Getenv("TEST_SSH_FAIL_PORT") &&
			slices.Contains(args, "UserKnownHostsFile=known_hosts") && len(args) > 0 && args[len(args)-1] == "agent@127.0.0.1"
		if readiness {
			os.Exit(255)
		}
		command := exec.Command(os.Getenv("TEST_SSH_REAL_EXECUTABLE"), args...)
		command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				os.Exit(exit.ExitCode())
			}
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		return
	}
	if prefix == "TEST_SSH_KEYGEN" && os.Getenv("TEST_SSH_KEYGEN_REAL_EXECUTABLE") != "" {
		command := exec.Command(os.Getenv("TEST_SSH_KEYGEN_REAL_EXECUTABLE"), os.Args[1:]...)
		command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				os.Exit(exit.ExitCode())
			}
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		return
	}
	fmt.Fprint(os.Stdout, os.Getenv(prefix+"_OUTPUT"))
	if prefix == "TEST_SSH" && os.Getenv("TEST_SSH_KNOWN_HOSTS") != "" {
		if err := os.WriteFile("known_hosts", []byte(os.Getenv("TEST_SSH_KNOWN_HOSTS")), 0600); err != nil {
			panic(err)
		}
	}
	code, err := strconv.Atoi(os.Getenv(prefix + "_EXIT"))
	if err != nil {
		code = 0
	}
	if script := os.Getenv(prefix + "_RESPONSES"); script != "" {
		var responses map[string]struct {
			FailOnce           bool
			Output             string
			Stderr             string
			Exit               int
			ReadyFile, WaitFor string
			HoldOutputUntil    string
			ExpectedStdin      string
			EchoStdin          bool
		}
		if err := json.Unmarshal([]byte(script), &responses); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		key := strings.Join(os.Args[1:], " ")
		response, ok := responses[key]
		if !ok {
			var keys []string
			for candidate := range responses {
				keys = append(keys, candidate)
			}
			slices.SortFunc(keys, func(a, b string) int {
				if len(a) != len(b) {
					return len(b) - len(a)
				}
				return strings.Compare(a, b)
			})
			for _, candidate := range keys {
				if strings.Contains(candidate, "*") && regexp.MustCompile("^"+strings.ReplaceAll(regexp.QuoteMeta(candidate), `\*`, ".*")+"$").MatchString(key) {
					response, ok = responses[candidate], true
					break
				}
			}
		}
		if ok {
			if response.FailOnce {
				data, _ := os.ReadFile(os.Getenv(prefix + "_LOG"))
				count := 0
				for _, line := range strings.Split(string(data), "\n") {
					var args []string
					if json.Unmarshal([]byte(line), &args) == nil && slices.Equal(args, os.Args[1:]) {
						count++
					}
				}
				if count > 1 {
					response.Exit = 0
					response.Output = ""
				}
			}
			if response.HoldOutputUntil != "" {
				executable, err := os.Executable()
				if err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(125)
				}
				child := exec.Command(executable, "--test-hold-output", response.HoldOutputUntil)
				child.Stdout, child.Stderr = os.Stdout, os.Stderr
				if err := child.Start(); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(125)
				}
				if err := child.Process.Release(); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(125)
				}
			}
			if response.ReadyFile != "" {
				if err := os.WriteFile(response.ReadyFile, nil, 0600); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(125)
				}
			}
			if response.WaitFor != "" {
				deadline := time.Now().Add(10 * time.Second)
				for {
					if _, err := os.Stat(response.WaitFor); err == nil {
						break
					}
					if time.Now().After(deadline) {
						fmt.Fprintln(os.Stderr, "fake rendezvous timed out")
						os.Exit(125)
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			fmt.Fprint(os.Stdout, response.Output)
			fmt.Fprint(os.Stderr, response.Stderr)
			if response.ExpectedStdin != "" {
				data, err := io.ReadAll(os.Stdin)
				if err != nil || string(data) != response.ExpectedStdin {
					fmt.Fprintln(os.Stderr, "fake stdin mismatch")
					os.Exit(125)
				}
				if response.EchoStdin {
					fmt.Fprint(os.Stdout, string(data))
				}
			} else if response.EchoStdin {
				if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(125)
				}
			}
			code = response.Exit
		} else {
			fmt.Fprintln(os.Stderr, "unscripted fake call", os.Args[1:])
			code = 125
		}
	}
	if path := os.Getenv(prefix + "_ENV_LOG"); path != "" {
		data, _ := json.Marshal(os.Environ())
		_ = os.WriteFile(path, data, 0600)
	}
	if prefix == "TEST_SSH_KEYGEN" && code == 0 && os.Getenv("TEST_SSH_KEYGEN_PUBLIC_KEY") != "" {
		for index, arg := range os.Args[1:] {
			if arg == "-f" && index+2 < len(os.Args) {
				path := os.Args[index+2]
				if err := os.WriteFile(path, []byte("test-owned dedicated private key\n"), 0600); err != nil {
					panic(err)
				}
				if err := os.WriteFile(path+".pub", []byte(os.Getenv("TEST_SSH_KEYGEN_PUBLIC_KEY")+"\n"), 0644); err != nil {
					panic(err)
				}
			}
		}
	}
	os.Exit(code)
}
