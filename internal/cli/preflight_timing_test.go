package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
	"github.com/grauzone-labs/sandboxed-agents/internal/preflighttiming"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// TestPreflightTimingObservesTheOuterWindowsCall proves a passing full preflight
// produces one closed observation without changing checks at the RunWithHost seam.
func TestPreflightTimingObservesTheOuterWindowsCall(t *testing.T) {
	f := windowsHost(t)
	directory := t.TempDir()
	manifest := `{"version":1,"kind":"live-preflight-timing","run":"` + strings.Repeat("a", 64) + `","revision":"` + strings.Repeat("b", 40) + `"}`
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("c", 32)
	t.Setenv("SANDBOXED_AGENTS_LIVE_PREFLIGHT_DIR", directory)
	t.Setenv("SANDBOXED_AGENTS_LIVE_PREFLIGHT_TOKEN", token)
	status, output, calls := f.run(t, "check")
	if status != 0 || !strings.Contains(output, "host prerequisites satisfied") || len(calls) != 5 || len(f.sshCalls(t)) != 6 {
		t.Fatalf("preflight changed: %d %s %v", status, output, calls)
	}
	data, err := os.ReadFile(filepath.Join(directory, "inv-"+token+".json"))
	if err != nil {
		t.Fatalf("actual preflight observation absent: %v", err)
	}
	var observation struct {
		Preflight *struct {
			State, Outcome string
			Seconds        *float64
		}
		TargetSelection any `json:"target_selection"`
		Returned        bool
	}
	if err = json.Unmarshal(data, &observation); err != nil {
		t.Fatal(err)
	}
	if observation.Preflight == nil || observation.Preflight.State != "closed" || observation.Preflight.Outcome != "pass" || observation.Preflight.Seconds == nil || observation.TargetSelection != nil || !observation.Returned {
		t.Fatalf("wrong outer observation %s", data)
	}
}

// timingCLI runs the real CLI with separate output streams and snapshots only fake query argv.
func timingCLI(t *testing.T, f *fixture, args ...string) (int, string, string, [][]string, [][]string) {
	t.Helper()
	data, _ := json.Marshal(f.responses)
	t.Setenv("TEST_PODMAN_RESPONSES", string(data))
	data, _ = json.Marshal(f.sshResponses)
	t.Setenv("TEST_SSH_RESPONSES", string(data))
	var out, diagnostics bytes.Buffer
	status := cli.RunWithHost(args, &out, &diagnostics, f.host)
	return status, out.String(), diagnostics.String(), testutil.Invocations(t, f.fakes.PodmanLog), f.sshCalls(t)
}

// TestPreflightTimingPreservesQueryAndFailureSemantics proves channel-on/off
// byte/query parity across Windows facts, absence, timeout and client host-key
// refusal, plus Linux, at the real RunWithHost and fake process seams.
func TestPreflightTimingPreservesQueryAndFailureSemantics(t *testing.T) {
	for _, scenario := range []string{"windows", "absent", "controller-failure", "timeout", "host-key", "no-machine", "ambiguous", "invalid-ssh", "missing-podman", "linux"} {
		t.Run(scenario, func(t *testing.T) {
			f := windowsHost(t)
			switch scenario {
			case "absent":
				f.sshResponses[windowsAutomountExists] = response{Exit: 1, Stderr: "warning\n"}
			case "controller-failure":
				f.sshResponses[windowsControllerRead] = response{Exit: 23, Stderr: `{"preflight":{"state":"closed","outcome":"pass","seconds":999999},"private":"forged-payload"}`}
			case "timeout":
				defer cli.SetMachineQueryTimeout(50 * time.Millisecond)()
				f.sshResponses[windowsAutomountExists] = response{DelayAfterOutputMS: 300, Exit: 1}
			case "host-key":
				f.sshResponses[windowsCgroupRead] = response{Exit: 255, Stderr: "Host key verification failed."}
			case "no-machine":
				f.responses["machine list --format json"] = response{Output: "[]"}
			case "ambiguous":
				f.responses["machine list --format json"] = response{Output: `[{"Name":"one","VMType":"wsl"},{"Name":"two","VMType":"wsl"}]`}
			case "invalid-ssh":
				f.responses["machine inspect chosen"] = response{Output: `[{"Name":"chosen","State":"running","Rootful":false,"SSHConfig":{"IdentityPath":"relative","Port":0,"RemoteUsername":"core"}}]`}
			case "missing-podman":
				if err := os.Remove(fakePath(f, "podman")); err != nil {
					t.Fatal(err)
				}
			case "linux":
				f = linuxHost(t)
			}
			t.Setenv(preflighttiming.DirEnv, "")
			t.Setenv(preflighttiming.TokenEnv, "")
			status, out, diagnostics, calls, ssh := timingCLI(t, f, "check")
			_ = os.Remove(f.fakes.PodmanLog)
			_ = os.Remove(f.fakes.SSHLog)
			dir, err := preflighttiming.Create(t.TempDir(), strings.Repeat("a", 40))
			if err != nil {
				t.Fatal(err)
			}
			ledger, err := preflighttiming.OpenLedger(dir, strings.Repeat("a", 40))
			if err != nil {
				t.Fatal(err)
			}
			defer ledger.Close()
			_, token, err := ledger.Launch("preflight", "preflight")
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(preflighttiming.DirEnv, dir)
			t.Setenv(preflighttiming.TokenEnv, token)
			second, secondOut, secondErr, secondCalls, secondSSH := timingCLI(t, f, "check")
			// The observer preserves exact diagnostics as well as query argv and order.
			if second != status || secondOut != out || secondErr != diagnostics || !reflect.DeepEqual(secondCalls, calls) || !reflect.DeepEqual(secondSSH, ssh) {
				t.Fatalf("observer changed %s: status %d/%d stdout %q/%q stderr %q/%q queries %v/%v ssh %v/%v", scenario, status, second, out, secondOut, diagnostics, secondErr, calls, secondCalls, ssh, secondSSH)
			}
			if scenario == "timeout" && (!strings.Contains(secondErr, "deadline exceeded") || !strings.Contains(diagnostics, "deadline exceeded")) {
				t.Fatal("timeout became missing config")
			}
			journal, err := preflighttiming.Read(dir, strings.Repeat("a", 40))
			if err != nil {
				t.Fatal(err)
			}
			if len(journal.Entries) != 1 {
				t.Fatalf("wrong invocation count: %+v", journal)
			}
			observation := journal.Entries[0].Invocation
			expected := "pass"
			if status != 0 {
				expected = "fail"
			}
			if observation.Preflight == nil || observation.Preflight.Outcome != expected || observation.TargetSelection != nil {
				t.Fatalf("wrong scope/outcome %+v", journal)
			}
			if scenario == "controller-failure" {
				data, err := os.ReadFile(filepath.Join(dir, "inv-"+token+".json"))
				if err != nil || bytes.Contains(data, []byte("forged-payload")) || *observation.Preflight.Seconds == 999999 {
					t.Fatal("query payload forged timing evidence")
				}
			}
		})
	}
}

// TestPreflightTimingDistinguishesSelectionAndNotEntered proves usage/help/version
// and every dispatch family observe at most its one existing outer scope at the CLI seam.
func TestPreflightTimingDistinguishesSelectionAndNotEntered(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--help"}, {"unknown"}, {"check"}, {"build"}, {"up", "demo"}, {"update", "demo"}, {"list"}, {"shell", "demo"}, {"remove", "demo"}, {"check", "demo"}, {"start", "demo"}, {"stop", "demo"}, {"restart", "demo"}, {"fingerprint", "demo"}, {"ssh-config", "demo"}, {"agents", "status", "demo", "opencode"}, {"tools", "status", "demo", "t3code"}, {"integrations", "status", "demo", "git"}} {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			f := windowsHost(t)
			// A failed first query stops lifecycle/build work but still enters its actual outer scope.
			f.responses["machine list --format json"] = response{Output: "[]"}
			dir, err := preflighttiming.Create(t.TempDir(), strings.Repeat("a", 40))
			if err != nil {
				t.Fatal(err)
			}
			ledger, err := preflighttiming.OpenLedger(dir, strings.Repeat("a", 40))
			if err != nil {
				t.Fatal(err)
			}
			defer ledger.Close()
			_, token, err := ledger.Launch("preflight", "inspection")
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(preflighttiming.DirEnv, dir)
			t.Setenv(preflighttiming.TokenEnv, token)
			status, _, _, _, _ := timingCLI(t, f, args...)
			journal, err := preflighttiming.Read(dir, strings.Repeat("a", 40))
			if err != nil {
				t.Fatal(err)
			}
			if len(journal.Entries) != 1 {
				t.Fatalf("entries %+v", journal)
			}
			inv := journal.Entries[0].Invocation
			notEntered := args[0] == "version" || args[0] == "--help" || status == 2
			full := args[0] == "check" && len(args) == 1 || args[0] == "build" || args[0] == "up" || args[0] == "update"
			if !inv.Returned || notEntered && (inv.Preflight != nil || inv.TargetSelection != nil) || !notEntered && full && (inv.Preflight == nil || inv.TargetSelection != nil) || !notEntered && !full && (inv.TargetSelection == nil || inv.Preflight != nil) {
				t.Fatalf("wrong dispatch observation %+v", inv)
			}
		})
	}
}

// TestPreflightTimingTracksBlockedWindowsCall proves the real outer preflight
// is open while a fake machine query blocks, then closes with a bounded measured
// interval and unchanged successful outcome at RunWithHost's process boundary.
func TestPreflightTimingTracksBlockedWindowsCall(t *testing.T) {
	f := windowsHost(t)
	const revision = "0123456789abcdef0123456789abcdef01234567"
	directory, err := preflighttiming.Create(t.TempDir(), revision)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := preflighttiming.OpenLedger(directory, revision)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	_, token, err := ledger.Launch("preflight", "preflight")
	if err != nil {
		t.Fatal(err)
	}
	ready, release := filepath.Join(t.TempDir(), "ready"), filepath.Join(t.TempDir(), "release")
	f.sshResponses[windowsUIDRead] = response{Output: "1000\n", ReadyFile: ready, WaitFor: release}
	data, _ := json.Marshal(f.responses)
	t.Setenv("TEST_PODMAN_RESPONSES", string(data))
	data, _ = json.Marshal(f.sshResponses)
	t.Setenv("TEST_SSH_RESPONSES", string(data))
	t.Setenv(preflighttiming.DirEnv, directory)
	t.Setenv(preflighttiming.TokenEnv, token)
	var out, diagnostics bytes.Buffer
	done := make(chan int, 1)
	started := time.Now()
	go func() { done <- cli.RunWithHost([]string{"check"}, &out, &diagnostics, f.host) }()
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case status := <-done:
			t.Fatalf("query did not block: status %d diagnostics %s", status, diagnostics.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("fake query never became ready")
		}
		time.Sleep(time.Millisecond)
	}
	enteredAt := time.Now()
	journal, err := preflighttiming.Read(directory, revision)
	if err != nil || len(journal.Entries) != 1 || journal.Entries[0].Invocation == nil || journal.Entries[0].Invocation.Preflight == nil || journal.Entries[0].Invocation.Preflight.State != "open" {
		t.Fatalf("blocked production call was not open: %+v %v", journal, err)
	}
	held := time.Now()
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-done:
		if status != 0 {
			t.Fatalf("released preflight: %s", diagnostics.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("released preflight did not finish")
	}
	wall := time.Since(started).Seconds()
	journal, err = preflighttiming.Read(directory, revision)
	if err != nil {
		t.Fatal(err)
	}
	phase := journal.Entries[0].Invocation.Preflight
	if phase.State != "closed" || phase.Outcome != "pass" || phase.Seconds == nil || *phase.Seconds > wall || *phase.Seconds < held.Sub(enteredAt).Seconds() {
		t.Fatalf("wrong real call interval: %+v, wall %f", phase, wall)
	}
}

// TestPreflightTimingInvalidChannelIsSilent proves malformed or unavailable
// private channels preserve exact Windows CLI output and queries and create no
// files, through RunWithHost and the isolated fake-program boundary.
func TestPreflightTimingInvalidChannelIsSilent(t *testing.T) {
	for _, scenario := range []string{"absent", "relative", "token", "manifest", "link"} {
		t.Run(scenario, func(t *testing.T) {
			f := windowsHost(t)
			t.Setenv(preflighttiming.DirEnv, "")
			t.Setenv(preflighttiming.TokenEnv, "")
			status, out, diagnostics, calls, ssh := timingCLI(t, f, "check")
			_ = os.Remove(f.fakes.PodmanLog)
			_ = os.Remove(f.fakes.SSHLog)
			directory := t.TempDir()
			channel, token := directory, strings.Repeat("a", 32)
			switch scenario {
			case "absent":
				token = ""
			case "relative":
				channel = "relative-channel"
			case "token":
				token = "../invalid"
			case "manifest":
				if err := os.WriteFile(filepath.Join(directory, "manifest.json"), []byte(`{"version":1,"private":"untrusted"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "link":
				channel = filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(directory, channel); err != nil {
					t.Skip("symlink creation unavailable")
				}
			}
			before, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(preflighttiming.DirEnv, channel)
			t.Setenv(preflighttiming.TokenEnv, token)
			second, secondOut, secondErr, secondCalls, secondSSH := timingCLI(t, f, "check")
			if second != status || secondOut != out || secondErr != diagnostics || !reflect.DeepEqual(secondCalls, calls) || !reflect.DeepEqual(secondSSH, ssh) {
				t.Fatal("invalid channel changed CLI behavior")
			}
			after, err := os.ReadDir(directory)
			if err != nil || len(before) != len(after) {
				t.Fatal("invalid channel created a file")
			}
		})
	}
}
