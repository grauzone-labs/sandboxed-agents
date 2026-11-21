package liverun_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
)

// fakeHost records process-boundary queries and supplies one complete idle engine.
type fakeHost struct {
	calls     []liverun.Invocation
	fail      string
	errorText string
	goVersion string
	answers   map[string]string
}

// Execute supplies literal host query answers without running a real engine or build.
func (f *fakeHost) Execute(_ context.Context, in liverun.Invocation) ([]byte, error) {
	f.calls = append(f.calls, in)
	key := in.Program + " " + strings.Join(in.Args, " ")
	for match, answer := range f.answers {
		if strings.Contains(key, match) {
			return []byte(answer), nil
		}
	}
	if strings.Contains(key, f.fail) && f.fail != "" {
		if f.errorText != "" {
			return nil, fmt.Errorf("%s", f.errorText)
		}
		return nil, fmt.Errorf("private host detail /secret/name")
	}
	switch {
	case in.Program == "git" && len(in.Args) > 0 && in.Args[0] == "status":
		return nil, nil
	case in.Program == "git":
		return []byte(strings.Repeat("a", 40) + "\n"), nil
	case in.Program == "go" && len(in.Args) > 0 && in.Args[0] == "env":
		v := f.goVersion
		if v == "" {
			v = "go1.27.2"
		}
		return []byte(v + "\n"), nil
	case strings.Contains(key, "machine list"):
		return []byte(`[{"Name":"selected","Default":true,"VMType":"wsl"}]`), nil
	case strings.Contains(key, "machine inspect"):
		return []byte(`[{"Name":"selected","State":"running","Rootful":false,"ConfigDir":{"Path":"C:\\private\\machine"},"SSHConfig":{"IdentityPath":"C:\\private\\key","Port":2222,"RemoteUsername":"user"}}]`), nil
	case strings.Contains(key, "info --format json"):
		return []byte(`{"host":{"hostname":"engine"},"store":{"graphRoot":"/private/storage"}}`), nil
	case strings.Contains(key, "images --all"):
		return []byte("[]"), nil
	case strings.Contains(key, "ps --all"):
		return []byte("[]"), nil
	case strings.Contains(key, "system df"):
		return []byte("TYPE TOTAL ACTIVE SIZE RECLAIMABLE\n"), nil
	case in.Program == "ssh":
		return []byte("Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/sda 1000 100 900 10% /\n"), nil
	}
	return nil, fmt.Errorf("unexpected process: %s", key)
}

// commandHarness creates an isolated run and a measured peak bound for the command seam.
func commandHarness(t *testing.T) (*liverun.Service, *fakeHost, []string, string) {
	t.Helper()
	root := t.TempDir()
	run := filepath.Join(root, "private")
	record := filepath.Join(root, "records")
	lock := filepath.Join(root, "locks")
	if err := os.MkdirAll(record, 0700); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(root, "checkout")
	if err := os.Mkdir(checkout, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "go.mod"), []byte("module fixture\n\ngo 1.27.2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	peak := filepath.Join(root, "peak.json")
	writePeakFixture(t, peak, false)
	fake := &fakeHost{}
	svc := &liverun.Service{Executor: fake, FreeSpace: func(string) (uint64, error) { return 900, nil }, Platform: "linux", CoordinationRoot: lock, SessionProbe: func() (liverun.SessionPolicy, error) { return liverun.SessionPolicy{}, nil }}
	args := []string{"prepare", "-checkout", checkout, "-run-dir", run, "-record-dir", record, "-revision", strings.Repeat("a", 40), "-peak-record", peak}
	return svc, fake, args, run
}

// TestPrepareCommandChecksRevisionAndHeadroom proves the prepare command saves an inventory without starting builds. It exercises the prepare command at the injected host boundary seam.
func TestPrepareCommandChecksRevisionAndHeadroom(t *testing.T) {
	svc, fake, args, run := commandHarness(t)
	var out bytes.Buffer
	if err := svc.RunDispatch(context.Background(), args, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"Prepared":true`) {
		t.Fatalf("prepare did not succeed: %s", out.String())
	}
	data, err := os.ReadFile(filepath.Join(run, "before.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot liverun.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.Complete || len(snapshot.Disks) != 1 || snapshot.Disks[0].FreeBytes != 900 {
		t.Fatalf("snapshot: %+v", snapshot)
	}
	for _, call := range fake.calls {
		for _, arg := range call.Args {
			if arg == "test" || arg == "build" {
				t.Fatalf("prepare started build: %+v", call)
			}
		}
	}
}

// TestPrepareCommandRefusesFailedChecks proves each pre-build check fails closed through the command seam.
func TestPrepareCommandRefusesFailedChecks(t *testing.T) {
	tests := []struct {
		name, check, fail, version string
		free                       uint64
		diskError                  bool
	}{
		{name: "revision", check: "revision", fail: "git rev-parse"},
		{name: "dirty checkout query", check: "checkout-clean", fail: "git status"},
		{name: "toolchain query", check: "go-version", fail: "go env"},
		{name: "toolchain mismatch", check: "go-version", version: "go1.27.1"},
		{name: "engine", check: "engine", fail: "info --format"},
		{name: "filesystem", check: "filesystem", diskError: true},
		{name: "disk usage", check: "storage-measurement", fail: "system df"},
		{name: "images", check: "inventory", fail: "images --all"},
		{name: "containers", check: "inventory", fail: "ps --all"},
		{name: "headroom equal to bound", check: "headroom", free: 100},
		{name: "headroom below bound", check: "headroom", free: 99},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc, fake, args, run := commandHarness(t)
			fake.fail, fake.goVersion = test.fail, test.version
			svc.FreeSpace = func(string) (uint64, error) {
				if test.diskError {
					return 0, fmt.Errorf("unreadable")
				}
				if test.free > 0 {
					return test.free, nil
				}
				return 900, nil
			}
			var out bytes.Buffer
			err := svc.RunDispatch(context.Background(), args, &out, &out)
			if err == nil || !strings.Contains(err.Error(), "check "+test.check+" failed") {
				t.Fatalf("wrong check failure: %v", err)
			}
			if strings.Contains(fmt.Sprint(err), "/secret") || strings.Contains(out.String(), "/secret") {
				t.Fatal("private diagnostics leaked")
			}
			if _, err := os.Stat(filepath.Join(run, "before.json")); !os.IsNotExist(err) {
				t.Fatal("failed prepare published complete snapshot")
			}
			for _, call := range fake.calls {
				for _, arg := range call.Args {
					if arg == "build" || arg == "test" {
						t.Fatalf("failed check started suite/build: %+v", call)
					}
				}
			}
		})
	}
}

// TestPrepareRequiresRecordedHostPeak proves absent or mismatched peak evidence refuses at the command seam.
func TestPrepareRequiresRecordedHostPeak(t *testing.T) {
	for _, payload := range []string{"", `{"EngineID":"other-host","PeakAdditionalBytes":100,"Basis":"earlier run"}`, `{"EngineID":"engine:/private/storage","PeakAdditionalBytes":0,"Basis":"net after cleanup"}`} {
		svc, _, args, run := commandHarness(t)
		peak := args[len(args)-1]
		if payload == "" {
			_ = os.Remove(peak)
		} else {
			if err := os.WriteFile(peak, []byte(payload), 0600); err != nil {
				t.Fatal(err)
			}
		}
		var out bytes.Buffer
		if err := svc.RunDispatch(context.Background(), args, &out, &out); err == nil || !strings.Contains(err.Error(), "peak-bound") {
			t.Fatalf("invalid peak accepted: %v", err)
		}
		if _, err := os.Stat(filepath.Join(run, "before.json")); !os.IsNotExist(err) {
			t.Fatal("invalid peak published snapshot")
		}
	}
}

// TestPrepareWindowsReadsBothFilesystems proves the command binds every engine query and records both headroom values. It exercises the prepare command at the injected host boundary seam.
func TestPrepareWindowsReadsBothFilesystems(t *testing.T) {
	svc, fake, args, run := commandHarness(t)
	svc.Platform = "windows"
	writePeakFixture(t, args[len(args)-1], true)
	svc.MachineDisk = func(string) (string, error) { return "C:\\private\\ext4.vhdx", nil }
	t.Setenv("CONTAINER_HOST", "ignored")
	t.Setenv("CONTAINER_CONNECTION", "ignored")
	t.Setenv("CONTAINER_SSHKEY", "ignored")
	var out bytes.Buffer
	if err := svc.RunDispatch(context.Background(), args, &out, &out); err != nil {
		t.Fatal(err)
	}
	var before liverun.Snapshot
	if err := json.Unmarshal(mustReadFile(t, filepath.Join(run, "before.json")), &before); err != nil {
		t.Fatal(err)
	}
	if len(before.Disks) != 2 || before.Disks[0].Name != "host" || before.Disks[1].Name != "engine" {
		t.Fatalf("missing Windows filesystems: %+v", before.Disks)
	}
	for _, call := range fake.calls {
		if call.Program == "podman" && call.Args[0] != "machine" {
			if len(call.Args) < 3 || call.Args[0] != "--connection" || call.Args[1] != "selected" {
				t.Fatalf("unbound engine query: %+v", call)
			}
		}
		for _, entry := range call.Env {
			if strings.HasPrefix(entry, "CONTAINER_HOST=") || strings.HasPrefix(entry, "CONTAINER_CONNECTION=") || strings.HasPrefix(entry, "CONTAINER_SSHKEY=") {
				t.Fatalf("ambient override leaked")
			}
		}
	}
}

// mustReadFile loads an expected artifact from the real temporary filesystem.
func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestCollectUsesBeforeSnapshotAndSanitizesOutput proves collect classifies the retained inventory rather than querying the changed engine. It exercises the collect command and retained artifacts seam.
func TestCollectUsesBeforeSnapshotAndSanitizesOutput(t *testing.T) {
	svc, fake, args, run := commandHarness(t)
	id := strings.Repeat("b", 64)
	asset := strings.Repeat("c", 64)
	fake.answers = map[string]string{
		"images --all":  `[{"Id":"` + id + `"}]`,
		"image inspect": `[{"Id":"sha256:` + id + `","Labels":{"io.sandboxed-agents.image":"true","io.sandboxed-agents.asset-sha256":"` + asset + `","io.sandboxed-agents.toolchains":"none"},"Created":"2026-10-01T12:00:00Z","RepoTags":["private-tag"]}]`,
	}
	var out bytes.Buffer
	if err := svc.RunDispatch(context.Background(), args, &out, &out); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "exit.json"), []byte(`{"Code":1,"Interrupted":false,"EndedAt":"2026-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "suite.log"), []byte("asset-sha256 "+asset+"\nprivate-group /secret/path\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fake.answers = map[string]string{"go run": filepath.Join(filepath.Dir(run), "records", "durations-linux.json") + "\n"}
	fake.calls = nil
	out.Reset()
	if err := svc.RunDispatch(context.Background(), []string{"collect", "-run-dir", run}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 || fake.calls[0].Program != "go" {
		t.Fatalf("collect queried changed engine: %+v", fake.calls)
	}
	command := strings.Join(fake.calls[0].Args, " ")
	if !strings.Contains(command, "-base-image present") || !strings.Contains(command, "-build-cache warm") || !strings.Contains(command, "-host-load unknown") {
		t.Fatalf("wrong snapshot context: %s", command)
	}
	for _, secret := range []string{"private-tag", "private-group", "/secret/path", filepath.Dir(run)} {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("collector output leaked: %s", out.String())
		}
	}
}

// TestStatusRejectsUnrecognizedPrivateProgress proves status cannot emit object names or paths from a malformed progress artifact. It exercises the status command and process/artifact boundary seam.
func TestStatusRejectsUnrecognizedPrivateProgress(t *testing.T) {
	svc, _, args, run := commandHarness(t)
	var out bytes.Buffer
	if err := svc.RunDispatch(context.Background(), args, &out, &out); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "progress.json"), []byte(`{"Case":"private-group","Operation":"/private/path/image-name","LastOutput":"2026-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := svc.RunDispatch(context.Background(), []string{"status", "-run-dir", run}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "private") || strings.Contains(out.String(), "image-name") {
		t.Fatalf("status leaked private evidence: %s", out.String())
	}
}

// TestPrepareWindowsRefusesInvalidMachineEvidence proves every target and disk failure prevents builds at the command seam.
func TestPrepareWindowsRefusesInvalidMachineEvidence(t *testing.T) {
	cases := []struct {
		name, key, answer, fail string
		diskError               bool
	}{
		{name: "list query", fail: "machine list"},
		{name: "inspect query", fail: "machine inspect"},
		{name: "empty list", key: "machine list", answer: "[]"},
		{name: "ambiguous default", key: "machine list", answer: `[{"Name":"one","VMType":"wsl"},{"Name":"two","VMType":"wsl"}]`},
		{name: "stopped", key: "machine inspect", answer: `[{"Name":"selected","State":"stopped","Rootful":false}]`},
		{name: "rootful", key: "machine inspect", answer: `[{"Name":"selected","State":"running","Rootful":true}]`},
		{name: "rootless unverified", key: "machine inspect", answer: `[{"Name":"selected","State":"running"}]`},
		{name: "bad machine name", key: "machine list", answer: `[{"Name":"-unsafe","Default":true,"VMType":"wsl"}]`},
		{name: "machine SSH failed", fail: "ssh "},
		{name: "machine filesystem malformed", key: "ssh ", answer: "unreadable filesystem"},
		{name: "host virtual disk unreadable", diskError: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			svc, fake, args, _ := commandHarness(t)
			svc.Platform = "windows"
			svc.MachineDisk = func(string) (string, error) {
				if test.diskError {
					return "", fmt.Errorf("disk unreadable")
				}
				return "C:\\private\\ext4.vhdx", nil
			}
			fake.fail = test.fail
			if test.key != "" {
				fake.answers = map[string]string{test.key: test.answer}
			}
			var out bytes.Buffer
			if err := svc.RunDispatch(context.Background(), args, &out, &out); err == nil {
				t.Fatal("invalid machine evidence was accepted")
			}
			for _, call := range fake.calls {
				for _, word := range call.Args {
					if word == "build" || word == "test" || word == "start" {
						t.Fatalf("prepare changed host: %+v", call)
					}
				}
			}
		})
	}
}

// TestCollectPreservesPreviousDurationsAfterFailure proves a failed collector cannot turn an old file into current evidence. It exercises the collect command and retained artifacts seam.
func TestCollectPreservesPreviousDurationsAfterFailure(t *testing.T) {
	svc, fake, args, run := commandHarness(t)
	var out bytes.Buffer
	if err := svc.RunDispatch(context.Background(), args, &out, &out); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "exit.json"), []byte(`{"Code":1,"Interrupted":false,"EndedAt":"2026-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "suite.log"), []byte("no completed observations\n"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := filepath.Join(filepath.Dir(run), "records", "durations-linux.json")
	if err := os.WriteFile(previous, []byte("previous invocation\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fake.fail = "go run"
	out.Reset()
	if err := svc.RunDispatch(context.Background(), []string{"collect", "-run-dir", run}, &out, &out); err == nil || !strings.Contains(err.Error(), "durations-collector") {
		t.Fatalf("collector failure lost: %v", err)
	}
	if len(out.Bytes()) != 0 || string(mustReadFile(t, previous)) != "previous invocation\n" {
		t.Fatal("old durations changed or reported as current")
	}
}

// TestPrepareRejectsNullInventories proves absence is claimed only from complete array answers at the command seam.
func TestPrepareRejectsNullInventories(t *testing.T) {
	for _, key := range []string{"images --all", "ps --all"} {
		t.Run(key, func(t *testing.T) {
			svc, fake, args, _ := commandHarness(t)
			fake.answers = map[string]string{key: "null"}
			var out bytes.Buffer
			if err := svc.RunDispatch(context.Background(), args, &out, &out); err == nil || !strings.Contains(err.Error(), "inventory") {
				t.Fatalf("null inventory accepted: %v", err)
			}
		})
	}
}

// writePeakFixture supplies a reasoned host-bound record and optional observations at the host-record boundary.
func writePeakFixture(t *testing.T, path string, windows bool) {
	t.Helper()
	p := liverun.PeakRecord{EngineID: "engine:/private/storage", Revision: strings.Repeat("a", 40), PeakAdditionalBytes: 100, Basis: "fixture allowances cover every growth source at these literal byte budgets", SampleStartedAt: time.Now().Add(-time.Minute), SampleEndedAt: time.Now().Add(-time.Second), SampleIntervalSeconds: 10, SampleContext: "isolated fixture", SamplingComplete: true}
	p.Filesystems = []liverun.PeakFilesystem{{Name: "engine", Path: "/private/storage", StartFreeBytes: 900, MinimumFreeBytes: 850, SampledGrowthBytes: 50, Samples: 2}}
	if windows {
		p.Filesystems = append(p.Filesystems, liverun.PeakFilesystem{Name: "host", Path: `C:\private\ext4.vhdx`, StartFreeBytes: 900, MinimumFreeBytes: 850, SampledGrowthBytes: 50, Samples: 2})
	}

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

// TestPrepareAcceptsReasonedLegacyPeak proves the prepare command honors the original host-bound record contract without mandatory new component fields. It exercises the prepare command at the injected host boundary seam.
func TestPrepareAcceptsReasonedLegacyPeak(t *testing.T) {
	svc, _, args, _ := commandHarness(t)
	payload := `{"EngineID":"engine:/private/storage","PeakAdditionalBytes":100,"Basis":"recorded peak of documented complete fixture run before cleanup"}`
	if err := os.WriteFile(args[len(args)-1], []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := svc.RunDispatch(context.Background(), args, &out, &out); err != nil {
		t.Fatalf("original peak format refused: %v", err)
	}
}

// TestPrepareRefusesRestrictiveSessionJob proves the prepare command refuses an unsafe launching job before any build or engine query. It exercises the prepare command at the injected host boundary seam.
func TestPrepareRefusesRestrictiveSessionJob(t *testing.T) {
	svc, fake, args, _ := commandHarness(t)
	svc.SessionProbe = func() (liverun.SessionPolicy, error) {
		return liverun.SessionPolicy{InJob: true, KillOnClose: true}, nil
	}
	var out bytes.Buffer
	err := svc.RunDispatch(context.Background(), args, &out, &out)
	if err == nil || !strings.Contains(err.Error(), "session-detachment") {
		t.Fatalf("restrictive session accepted: %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatal("unsafe session reached host engine/build boundary")
	}
}

// TestPrepareRejectsBoundBelowObservedPeak proves the prepare command/artifact seam preserves the supplied record but refuses a bound contradicted by matching observed filesystem facts.
func TestPrepareRejectsBoundBelowObservedPeak(t *testing.T) {
	svc, _, args, run := commandHarness(t)
	var options liverun.Options
	var out bytes.Buffer
	if err := svc.RunDispatch(context.Background(), args, &out, &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(mustReadFile(t, filepath.Join(run, "options.json")), &options); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("engine:/private/storage"))
	observed := filepath.Join(options.LockRoot, fmt.Sprintf("%x-observed-peak.json", digest))
	payload := liverun.PeakRecord{EngineID: "engine:/private/storage", Filesystems: []liverun.PeakFilesystem{{Name: "engine", Path: "/private/storage", SampledGrowthBytes: 101, Samples: 2}}}
	cleanupArtifact(t, options.LockRoot, filepath.Base(observed), payload)
	bound := mustReadFile(t, args[len(args)-1])
	out.Reset()
	if err := svc.RunDispatch(context.Background(), args, &out, &out); err == nil || !strings.Contains(err.Error(), "peak-bound") {
		t.Fatalf("known undersized bound accepted: %v", err)
	}
	if !bytes.Equal(bound, mustReadFile(t, args[len(args)-1])) {
		t.Fatal("admitted source record was overwritten")
	}
}

// TestStatusSeparatesObservedPeakAndAdmittedBound proves the status command/artifact seam reports a retained observation independently of the original admitted bound.
func TestStatusSeparatesObservedPeakAndAdmittedBound(t *testing.T) {
	svc, _, args, run := commandHarness(t)
	var out bytes.Buffer
	if err := svc.RunDispatch(context.Background(), args, &out, &out); err != nil {
		t.Fatal(err)
	}
	cleanupArtifact(t, run, "exit.json", map[string]any{"Code": 0, "Interrupted": false, "EndedAt": time.Now().UTC()})
	cleanupArtifact(t, run, "peak.json", liverun.PeakRecord{EngineID: "engine:/private/storage", Revision: strings.Repeat("a", 40)})
	out.Reset()
	if err := svc.RunDispatch(context.Background(), []string{"status", "-run-dir", run}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"peak_recorded":true`) || !strings.Contains(out.String(), `"peak_bound_available":true`) {
		t.Fatalf("admitted bound conflated with observation: %s", &out)
	}
}

// TestUnboundedDecisionCannotBypassKnownSpace proves the prepare command/filesystem seam blocks known bounds and observed lower peaks on either Windows filesystem and Linux.
func TestUnboundedDecisionCannotBypassKnownSpace(t *testing.T) {
	for _, platform := range []string{"linux", "windows-host", "windows-engine"} {
		for _, source := range []string{"bound", "observed", "contradicted"} {
			t.Run(platform+"/"+source, func(t *testing.T) {
				svc, fake, args, run := commandHarness(t)
				args = append(args, "-allow-unbounded", "-unbounded-decision", "fixture missing-bound decision")
				role, path := "engine", "/private/storage"
				svc.FreeSpace = func(string) (uint64, error) { return 100, nil }
				if strings.HasPrefix(platform, "windows") {
					svc.Platform = "windows"
					svc.MachineDisk = func(string) (string, error) { return `C:\private\ext4.vhdx`, nil }
					if platform == "windows-host" {
						role, path = "host", `C:\private\ext4.vhdx`
					} else {
						svc.FreeSpace = func(string) (uint64, error) { return 900, nil }
						fake.answers = map[string]string{"ssh ": "Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/sda 1000 900 100 90% /\n"}
					}
				}
				boundPath := ""
				for i, arg := range args {
					if arg == "-peak-record" {
						boundPath = args[i+1]
					}
				}
				if source == "observed" {
					if err := os.Remove(boundPath); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.WriteFile(boundPath, []byte(`{"EngineID":"engine:/private/storage","PeakAdditionalBytes":100,"Basis":"recorded conservative fixture peak"}`), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if source != "bound" {
					if err := os.MkdirAll(svc.CoordinationRoot, 0700); err != nil {
						t.Fatal(err)
					}
					digest := sha256.Sum256([]byte("engine:/private/storage"))
					cleanupArtifact(t, svc.CoordinationRoot, fmt.Sprintf("%x-observed-peak.json", digest), liverun.PeakRecord{EngineID: "engine:/private/storage", Filesystems: []liverun.PeakFilesystem{{Name: role, Path: path, SampledGrowthBytes: func() uint64 {
						if source == "contradicted" {
							return 101
						}
						return 100
					}(), Samples: 2}}})
				}
				var out bytes.Buffer
				err := svc.RunDispatch(context.Background(), args, &out, &out)
				if err == nil || !strings.Contains(err.Error(), "headroom") {
					t.Fatalf("known insufficiency bypassed: %v %s", err, &out)
				}
				if _, err := os.Stat(filepath.Join(run, "before.json")); !os.IsNotExist(err) {
					t.Fatal("failed headroom published complete preparation")
				}
				for _, call := range fake.calls {
					for _, arg := range call.Args {
						if arg == "test" || arg == "build" {
							t.Fatal("known insufficiency reached build")
						}
					}
				}
			})
		}
	}
}

// TestRunNamesPermissionDeniedLaunch proves the public run/start boundary maps a real permission-shaped failure to session-detachment without publishing a started suite.
func TestRunNamesPermissionDeniedLaunch(t *testing.T) {
	svc, _, args, run := commandHarness(t)
	var out bytes.Buffer
	if err := svc.RunDispatch(context.Background(), args, &out, &out); err != nil {
		t.Fatal(err)
	}
	calls := 0
	svc.StartSupervisor = func(*exec.Cmd) error {
		calls++
		return &os.PathError{Op: "CreateProcess", Path: "/private/sentinel", Err: os.ErrPermission}
	}
	out.Reset()
	err := svc.RunDispatch(context.Background(), []string{"run", "-run-dir", run}, &out, &out)
	if calls != 1 || err == nil || !strings.Contains(err.Error(), "session-detachment") || strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("wrong launch failure: calls=%d err=%v", calls, err)
	}
	if _, err := os.Stat(filepath.Join(run, "started.json")); !os.IsNotExist(err) {
		t.Fatal("failed launch published startup")
	}
}

// TestUnboundedDecisionRefusesUnreadableFactsAndZeroSpace proves the prepare command/filesystem seam cannot waive unreadable existing facts or a measured zero-free filesystem.
func TestUnboundedDecisionRefusesUnreadableFactsAndZeroSpace(t *testing.T) {
	for _, kind := range []string{"bound", "observed", "zero"} {
		t.Run(kind, func(t *testing.T) {
			svc, _, args, _ := commandHarness(t)
			bound := args[len(args)-1]
			args = append(args, "-allow-unbounded", "-unbounded-decision", "fixture no-bound decision")
			if kind == "bound" {
				if err := os.Remove(bound); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(bound, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "observed" {
				if err := os.MkdirAll(svc.CoordinationRoot, 0700); err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256([]byte("engine:/private/storage"))
				if err := os.Mkdir(filepath.Join(svc.CoordinationRoot, fmt.Sprintf("%x-observed-peak.json", digest)), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "zero" {
				if err := os.Remove(bound); err != nil {
					t.Fatal(err)
				}
				svc.FreeSpace = func(string) (uint64, error) { return 0, nil }
			}
			var out bytes.Buffer
			if err := svc.RunDispatch(context.Background(), args, &out, &out); err == nil {
				t.Fatal("unbounded decision waived known/unreadable capacity")
			}
		})
	}
}
