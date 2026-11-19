package main_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLiveCLIProcessFixture supplies bounded child/grandchild processes at the liveHost.cliContext
// subprocess seam so parent tests prove owned-descendant cancellation and combined-stream ordering.
// It returns immediately when unconfigured; the parent tests make the behaviour assertions.
func TestLiveCLIProcessFixture(t *testing.T) {
	mode := os.Getenv("TEST_LIVE_OWNED_FIXTURE")
	if mode == "" {
		return
	}
	marker := os.Getenv("TEST_LIVE_OWNED_MARKER")
	if mode == "framed" {
		for _, part := range []string{"split ", "line\nlive operation image build: started\n", "\rlive operation rebuild: finished\n", strings.Repeat("x", 8200), "tail"} {
			fmt.Fprint(os.Stdout, part)
			time.Sleep(20 * time.Millisecond)
		}
		os.Exit(0)
	}
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

// TestLiveCLIFramesPayloadLines proves split writes, marker-shaped payload, oversized
// lines and final partial lines stay escaped while cliContext returns the exact trace.
func TestLiveCLIFramesPayloadLines(t *testing.T) {
	environment := append(os.Environ(), "TEST_LIVE_OWNED_FIXTURE=framed")
	for key, value := range nestedExecutables() {
		environment = append(environment, key+"="+value)
	}
	var records []string
	host := liveHost{binary: os.Args[0], environment: environment, report: func(line string) { records = append(records, line) }}
	output, err := host.cliContext(context.Background(), "", "-test.run=^TestLiveCLIProcessFixture$")
	want := "split line\nlive operation image build: started\n\rlive operation rebuild: finished\n" + strings.Repeat("x", 8200) + "tail"
	if err != nil || output != want {
		t.Fatalf("captured output=%q err=%v", output, err)
	}
	wantPayload := []string{"split line\n", "live operation image build: started\n", "\rlive operation rebuild: finished\n", strings.Repeat("x", 4096), strings.Repeat("x", 4096), "xxxxxxxxtail"}
	if len(records) != len(wantPayload)+2 || records[0] != "live operation inspection: started" || !strings.HasPrefix(records[len(records)-1], "live operation inspection: finished after ") {
		t.Fatalf("control/payload record counts: %q", records)
	}
	for index, payload := range wantPayload {
		if records[index+1] != "live output: "+strconv.QuoteToASCII(payload) || strings.ContainsAny(records[index+1], "\r\n") {
			t.Errorf("record %d=%q; want framed %q", index, records[index+1], payload)
		}
	}
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
	host := liveHost{binary: os.Args[0], environment: environment, timings: &liveTimings{}, caseName: "lifecycle"}
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
	operations := host.timings.evidence.Operations
	if len(operations) != 1 || operations[0].Result != "fail" || operations[0].Source != "cli" || operations[0].Case != "lifecycle" || operations[0].Seconds <= 0 {
		t.Fatalf("cancelled timing: %+v", operations)
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
	host := liveHost{binary: os.Args[0], environment: environment, timings: &liveTimings{}, caseName: "lifecycle"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := host.cliContext(ctx, "", "-test.run=^TestLiveCLIProcessFixture$")
	operations := host.timings.evidence.Operations
	if len(operations) != 1 || operations[0].Result != "pass" {
		t.Fatalf("completed timing: %+v", operations)
	}
	if err != nil || out != "first\nsecond\nthird\n" {
		t.Fatalf("output=%q error=%v", out, err)
	}
}
