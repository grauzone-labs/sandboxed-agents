package main_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// TestLiveRemoveGroupLockContract proves sandbox remove refuses an already held
// group SSH lock at the real CLI seam with isolated host state and fake Podman.
func TestLiveRemoveGroupLockContract(t *testing.T) {
	fakes := lifecycleHarness(t, ownedLifecycleResponses(true))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	holdSSHConfigLock(t, os.Getenv("XDG_STATE_HOME"), "default")
	status, output := lifecycleRun("remove", "demo", "--volumes", "--force")
	if status == 0 || !strings.Contains(output, `controller group "default": another SSH setup command is in progress; retry`) {
		t.Fatalf("held group lock: status=%d output=%q", status, output)
	}
	if len(testutil.Invocations(t, fakes.PodmanLog)) == 0 {
		t.Fatal("remove did not reach fake Podman")
	}
}

// TestLiveRemoveCoordinatesCaseHosts proves two case host copies queue their
// remove operations instead of hitting the group lock's nonblocking refusal and
// record one invocation timing each, through an atomic fake lock held across a
// scripted CLI process handshake.
func TestLiveRemoveCoordinatesCaseHosts(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("atomic shell fake uses Linux /bin/sh; Windows compiles the same host gate")
	}
	fakes := testutil.Install(t, fakeExecutable)
	t.Setenv("PATH", fakes.Dir)
	directory := t.TempDir()
	engine := liveFakeProgram(t)
	program := filepath.Join(directory, "remove-fixture")
	const script = `#!/bin/sh
if ! (set -C; : > "$TEST_REMOVE_LOCK") 2>/dev/null; then
 printf '%s\n' 'controller group "live-remove": another SSH setup command is in progress; retry' >&2
 exit 1
fi
trap '/bin/rm -f -- "$TEST_REMOVE_LOCK"' EXIT
"$TEST_REMOVE_ENGINE" "$@"
`
	if err := os.WriteFile(program, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ready, release := filepath.Join(directory, "ready"), filepath.Join(directory, "release")
	t.Setenv("SANDBOXED_AGENTS_GROUP", "live-remove")
	t.Setenv(liveRecordDirEnv, directory)
	suite := newLiveSuite(t)
	suite.host.binary = program
	suite.host.environment = testutil.Env(fakes.Env(os.Environ()), map[string]string{"TEST_REMOVE_LOCK": filepath.Join(directory, "lock"), "TEST_REMOVE_ENGINE": engine, "TEST_SANDBOXED_AGENTS_LOG": filepath.Join(directory, "cli.jsonl"), "TEST_SANDBOXED_AGENTS_RESPONSES": `{"remove first --volumes":{"ReadyFile":` + quoteLiveJSON(ready) + `,"WaitFor":` + quoteLiveJSON(release) + `},"remove second --volumes":{}}`})
	first, second := suite.host, suite.host
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	blocked := make(chan struct{}, 1)
	second.report = func(line string) {
		if strings.Contains(line, "waiting for another suite remove") {
			blocked <- struct{}{}
		}
	}
	go func() { _, err := first.cli("", "remove", "first", "--volumes"); firstDone <- err }()
	waitLiveMarker(t, ready)
	go func() { _, err := second.cli("", "remove", "second", "--volumes"); secondDone <- err }()
	finished := false
	select {
	case err := <-secondDone:
		finished = true
		t.Errorf("second remove reached the held group lock: %v", err)
	case <-blocked:
	case <-time.After(30 * time.Second):
		t.Error("second remove neither queued nor completed")
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; err != nil {
		t.Error(err)
	}
	if !finished {
		if err := <-secondDone; err != nil {
			t.Error(err)
		}
	}
	calls := testutil.Invocations(t, filepath.Join(directory, "cli.jsonl"))
	if len(calls) != 2 {
		t.Fatalf("remove calls %v; want both invocations retained", calls)
	}
	if timings := suite.host.timings.evidence.Operations; len(timings) != 2 || timings[0].Operation != "cleanup" || timings[1].Operation != "cleanup" {
		t.Errorf("queued removes recorded %+v; want one cleanup invocation each", timings)
	}
}

// TestLiveRemoveWaitPreservesCancellation proves a queued suite removal obeys
// its context, reports not-started and records neither an invocation timing nor
// engine diagnostics while retaining its control markers, through the held gate
// and fake CLI seam.
func TestLiveRemoveWaitPreservesCancellation(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	t.Setenv("PATH", fakes.Dir)
	t.Setenv("SANDBOXED_AGENTS_GROUP", "live-remove")
	t.Setenv(liveRecordDirEnv, t.TempDir())
	suite := newLiveSuite(t)
	host := suite.host
	host.binary = liveFakeProgram(t)
	host.environment = fakes.Env(os.Environ())
	host.removals <- struct{}{}
	defer func() { <-host.removals }()
	blocked := make(chan struct{}, 1)
	var events []string
	host.report = func(line string) {
		events = append(events, line)
		if strings.Contains(line, "waiting for another suite remove") {
			blocked <- struct{}{}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := host.cliContext(ctx, "", "remove", "queued", "--volumes"); done <- err }()
	select {
	case <-blocked:
	case <-time.After(30 * time.Second):
		t.Fatal("remove did not wait for the held gate")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued removal: %v", err)
		}
		if strings.Contains(err.Error(), "engine operations may still be running") || !strings.Contains(err.Error(), "host invocation was not started: suite remove gate") {
			t.Errorf("queued removal implied an actual invocation: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("queued remove ignored cancellation")
	}
	if timings := host.timings.evidence.Operations; len(timings) != 0 {
		t.Errorf("a removal that never launched recorded an invocation: %+v", timings)
	}
	if len(host.failures.evidence.Samples) != 0 {
		t.Errorf("queue wait consumed engine diagnostic samples: %+v", host.failures.evidence.Samples)
	}
	if !strings.Contains(strings.Join(events, "\n"), "live operation cleanup: started") || !strings.Contains(strings.Join(events, "\n"), "live operation cleanup: finished after") {
		t.Errorf("queued failure control markers lost: %v", events)
	}
	assertNoExternalCalls(t, fakes)
}

// TestLiveCLICancelledBeforeLaunchStartsNothing proves a remove whose context is
// already done when it acquires a free gate starts no invocation, timing record or
// engine diagnostics and does not claim engine work may still run, at the cliContext seam.
func TestLiveCLICancelledBeforeLaunchStartsNothing(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	t.Setenv("PATH", fakes.Dir)
	t.Setenv("SANDBOXED_AGENTS_GROUP", "live-remove")
	t.Setenv(liveRecordDirEnv, t.TempDir())
	suite := newLiveSuite(t)
	host := suite.host
	host.binary = liveFakeProgram(t)
	host.environment = fakes.Env(os.Environ())
	var events []string
	host.report = func(line string) { events = append(events, line) }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := host.cliContext(ctx, "", "remove", "cancelled", "--volumes")
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "engine operations may still be running") || !strings.Contains(err.Error(), "host invocation was not started: cancelled before launch") {
		t.Fatalf("cancelled removal: %v", err)
	}
	if timings := host.timings.evidence.Operations; len(timings) != 0 {
		t.Errorf("a removal that never launched recorded an invocation: %+v", timings)
	}
	if len(host.failures.evidence.Samples) != 0 {
		t.Errorf("a removal that never launched sampled the engine: %+v", host.failures.evidence.Samples)
	}
	if len(host.removals) != 0 {
		t.Error("the cancelled removal kept the suite remove gate")
	}
	if !strings.Contains(strings.Join(events, "\n"), "live operation cleanup: finished after") {
		t.Errorf("cancelled removal control markers lost: %v", events)
	}
	assertNoExternalCalls(t, fakes)
}
