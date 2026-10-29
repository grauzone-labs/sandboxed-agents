package main_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveCLIProcessFixture supplies bounded host child/grandchild processes for offline live-harness tests.
func TestLiveCLIProcessFixture(t *testing.T) {
	mode := os.Getenv("TEST_LIVE_OWNED_FIXTURE")
	if mode == "" {
		return
	}
	marker := os.Getenv("TEST_LIVE_OWNED_MARKER")
	if mode == "normal" {
		fmt.Fprintln(os.Stdout, "first")
		time.Sleep(20 * time.Millisecond)
		fmt.Fprintln(os.Stderr, "second")
		time.Sleep(20 * time.Millisecond)
		fmt.Fprintln(os.Stdout, "third")
		os.Exit(0)
	}
	if mode == "tree" {
		child := exec.Command(os.Args[0], "-test.run=^TestLiveCLIProcessFixture$")
		child.Env = append(os.Environ(), "TEST_LIVE_OWNED_FIXTURE=heartbeat")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(125)
		}
		fmt.Fprintln(os.Stdout, "CLI child started")
		_ = child.Wait()
		os.Exit(0)
	}
	for n := 0; n < 500; n++ {
		if _, err := os.Stat(marker + ".release"); err == nil {
			os.Exit(0)
		}
		_ = os.WriteFile(marker, []byte(fmt.Sprint(n)), 0600)
		time.Sleep(20 * time.Millisecond)
	}
	os.Exit(0)
}

// TestLiveCLICancellationOwnsHostDescendants proves the actual liveHost command path ends its
// local descendants on cancellation, preserves prior output and reports remote-engine uncertainty.
func TestLiveCLICancellationOwnsHostDescendants(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "heartbeat")
	t.Cleanup(func() { _ = os.WriteFile(marker+".release", nil, 0600); time.Sleep(50 * time.Millisecond) })
	environment := append(os.Environ(), "TEST_LIVE_OWNED_FIXTURE=tree", "TEST_LIVE_OWNED_MARKER="+marker)
	for key, value := range nestedExecutables() {
		environment = append(environment, key+"="+value)
	}
	host := liveHost{binary: os.Args[0], environment: environment}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		output string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		out, err := host.cliContext(ctx, "", "-test.run=^TestLiveCLIProcessFixture$")
		done <- result{out, err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("grandchild did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case r := <-done:
		if !errors.Is(r.err, context.Canceled) || !strings.Contains(r.output, "CLI child started") || !strings.Contains(r.err.Error(), "engine operations may still be running") {
			t.Fatalf("output=%s error=%v", r.output, r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("live CLI cancellation exceeded bound")
	}
	time.Sleep(100 * time.Millisecond)
	first, _ := os.ReadFile(marker)
	time.Sleep(150 * time.Millisecond)
	last, _ := os.ReadFile(marker)
	if string(first) != string(last) {
		t.Fatalf("live CLI left descendant alive: %s -> %s", first, last)
	}
}

// TestLiveCLIPreservesCombinedTrace proves normal CLI completion keeps stdout/stderr writes in
// their observed order through the same capture and supervision used by real live commands.
func TestLiveCLIPreservesCombinedTrace(t *testing.T) {
	environment := append(os.Environ(), "TEST_LIVE_OWNED_FIXTURE=normal")
	for key, value := range nestedExecutables() {
		environment = append(environment, key+"="+value)
	}
	host := liveHost{binary: os.Args[0], environment: environment}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := host.cliContext(ctx, "", "-test.run=^TestLiveCLIProcessFixture$")
	if err != nil || out != "first\nsecond\nthird\n" {
		t.Fatalf("output=%q error=%v", out, err)
	}
}
