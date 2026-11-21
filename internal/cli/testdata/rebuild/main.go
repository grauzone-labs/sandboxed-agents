// Program rebuild is a handshake-controlled fake Podman, delegating only queries to another fake.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// main records build intervals and output around explicit releases without ever calling an engine.
func main() {
	args := os.Args[1:]
	// An opted-in base tag answers from a file the test replaces, modelling another writer of the tag.
	if base := os.Getenv("REBUILD_FAKE_BASE_TAG"); base != "" && len(args) == 3 && args[0] == "image" && args[1] == "inspect" && args[2] == base {
		id, err := os.ReadFile(filepath.Join(os.Getenv("REBUILD_FAKE_DIRECTORY"), "base.id"))
		if err != nil {
			panic(err)
		}
		fmt.Printf(`[{"Id":%q}]`, id)
		return
	}
	if len(args) == 0 || args[0] != "build" {
		command := exec.Command(os.Getenv("REBUILD_FAKE_DELEGATE"), args...)
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				os.Exit(exit.ExitCode())
			}
			panic(err)
		}
		return
	}
	name := ""
	for _, arg := range args {
		if value, ok := strings.CutPrefix(arg, "io.sandboxed-agents.toolchains="); ok {
			name = value
		}
	}
	if name == "" {
		panic("build without toolchain label")
	}
	dir := os.Getenv("REBUILD_FAKE_DIRECTORY")
	data, _ := json.Marshal(args)
	mustWrite(filepath.Join(dir, name+".argv"), data)
	event(dir, "+"+name)
	fmt.Println("begin", name)
	fmt.Fprintln(os.Stderr, "stderr", name)
	mustWrite(filepath.Join(dir, name+".started"), nil)
	waitFor(filepath.Join(dir, name+".release"))
	if count, err := strconv.Atoi(os.Getenv("REBUILD_FAKE_PAYLOAD_BYTES")); err == nil {
		chunk := []byte(strings.Repeat("X", 32768))
		for count > 0 {
			n := min(count, len(chunk))
			if _, err := os.Stdout.Write(chunk[:n]); err != nil {
				panic(err)
			}
			count -= n
		}
	}
	fmt.Println("end", name)
	event(dir, "-"+name)
	if strings.Contains(","+os.Getenv("REBUILD_FAKE_FAILURES")+",", ","+name+",") {
		os.Exit(42)
	}
	mustWrite(filepath.Join(dir, name+".tag"), []byte("new-"+name))
}

// mustWrite publishes a fake-engine fact or fails the fixture immediately.
func mustWrite(path string, data []byte) {
	if err := os.WriteFile(path, data, 0600); err != nil {
		panic(err)
	}
}

// waitFor waits for a test-controlled release with a bounded failure deadline.
func waitFor(path string) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	panic("missing release: " + path)
}

// event appends one start or exit event under a cross-process fixture lock.
func event(dir, value string) {
	lock := filepath.Join(dir, "events.lock")
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := os.Mkdir(lock, 0700)
		if err == nil {
			break
		}
		if !os.IsExist(err) || time.Now().After(deadline) {
			panic(err)
		}
		time.Sleep(time.Millisecond)
	}
	defer os.Remove(lock)
	file, err := os.OpenFile(filepath.Join(dir, "events"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	defer file.Close()
	if _, err := fmt.Fprintln(file, value); err != nil {
		panic(err)
	}
}
