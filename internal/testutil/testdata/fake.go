// This executable is built by the test harness, never shipped with the CLI.
package main

import (
	"bufio"
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

// scriptedResponse is one scripted answer of the fake for an argv key.
type scriptedResponse struct {
	DelayAfterOutputMS int
	FailOnce           bool
	Output             string
	Stderr             string
	Exit               int
	ReadyFile, WaitFor string
	HoldOutputUntil    string
	ExpectedStdin      string
	EchoStdin          bool
	EchoLine           bool
	ExitOnInput        string
	Sequence           []scriptedAnswer
}

// scriptedAnswer is one answer of a sequence for the same argv.
type scriptedAnswer struct {
	Output string
	Stderr string
	Exit   int
}

// priorInvocations counts how often this exact argv was logged, this call included.
func priorInvocations(prefix string) int {
	data, _ := os.ReadFile(os.Getenv(prefix + "_LOG"))
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		var args []string
		if json.Unmarshal([]byte(line), &args) == nil && slices.Equal(args, os.Args[1:]) {
			count++
		}
	}
	return count
}

// main records process arguments and serves scripted responses for external-command tests.
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
	// Tests that care where a process ran, such as a private known_hosts directory, read one
	// working directory per call from the optional working-directory log.
	if path := os.Getenv(prefix + "_CWD_LOG"); path != "" {
		directory, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		log, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		fmt.Fprintln(log, directory)
		log.Close()
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
	// A scripted answer for this argv replaces the environment-driven output and status. An argv
	// outside the script is a test failure, with one scoped exception: an ssh call that names an
	// -E log file, the shape of the readiness probe and the sandbox SSH check, stays
	// environment-driven beside scripted machine queries, which name no log file.
	var response scriptedResponse
	scripted := false
	readinessShaped := prefix == "TEST_SSH" && slices.Index(os.Args, "-E") > 0
	if script := os.Getenv(prefix + "_RESPONSES"); script != "" {
		var responses map[string]scriptedResponse
		if err := json.Unmarshal([]byte(script), &responses); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		key := strings.Join(os.Args[1:], " ")
		response, scripted = responses[key]
		if !scripted {
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
					response, scripted = responses[candidate], true
					break
				}
			}
		}
		if !scripted && !readinessShaped {
			fmt.Fprintln(os.Stderr, "unscripted fake call", os.Args[1:])
			os.Exit(125)
		}
	}
	code := 0
	if scripted {
		if response.FailOnce && priorInvocations(prefix) > 1 {
			response.Exit = 0
			response.Output = ""
		}
		if len(response.Sequence) > 0 {
			// The same argv answers differently over time; the last answer repeats.
			answer := response.Sequence[min(priorInvocations(prefix)-1, len(response.Sequence)-1)]
			response.Output, response.Stderr, response.Exit = answer.Output, answer.Stderr, answer.Exit
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
		time.Sleep(time.Duration(response.DelayAfterOutputMS) * time.Millisecond)
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
		if response.EchoLine {
			// One typed line proves a terminal forwards input without needing an end of file.
			line, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(125)
			}
			fmt.Fprintf(os.Stdout, "line: %s\n", strings.TrimRight(line, "\r\n"))
		}
		if response.ExitOnInput != "" {
			// Model a terminal program that returns on a key sequence, such as a tmux detach.
			reader := bufio.NewReader(os.Stdin)
			var typed []byte
			for !strings.HasSuffix(string(typed), response.ExitOnInput) {
				next, err := reader.ReadByte()
				if err != nil {
					fmt.Fprintln(os.Stderr, "fake input ended before", strconv.Quote(response.ExitOnInput))
					os.Exit(125)
				}
				typed = append(typed, next)
			}
		}
		code = response.Exit
	} else {
		// Like ssh -E, a log file named after -E receives the client's output instead of stdout.
		if logFile := slices.Index(os.Args, "-E"); prefix == "TEST_SSH" && logFile > 0 && logFile+1 < len(os.Args) {
			if err := os.WriteFile(os.Args[logFile+1], []byte(os.Getenv(prefix+"_OUTPUT")), 0600); err != nil {
				panic(err)
			}
		} else {
			fmt.Fprint(os.Stdout, os.Getenv(prefix+"_OUTPUT"))
		}
		if prefix == "TEST_SSH" && os.Getenv("TEST_SSH_KNOWN_HOSTS") != "" {
			if err := os.WriteFile("known_hosts", []byte(os.Getenv("TEST_SSH_KNOWN_HOSTS")), 0600); err != nil {
				panic(err)
			}
		}
		if parsed, err := strconv.Atoi(os.Getenv(prefix + "_EXIT")); err == nil {
			code = parsed
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
