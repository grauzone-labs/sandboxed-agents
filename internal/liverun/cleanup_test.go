package liverun_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
)

// cleanupFake supplies an engine at the injected process boundary without executing Podman.
type cleanupFake struct {
	calls            []liverun.Invocation
	produced         []liverun.BuildResult
	unreachable      bool
	images           map[string]cleanupFakeImage
	containers       []liverun.Container
	fail             string
	revision         string
	engine           string
	psCalls          int
	lateReference    string
	collectorFail    bool
	keepAfterRemoval bool
	infoCalls        int
	disconnectAfter  int
}

// cleanupFakeImage supplies the literal image inspect payload, including size and parent.
type cleanupFakeImage struct {
	liverun.Image
	Parent string
	Size   uint64
}

// Execute returns literal whole-engine query answers and emulates only exact individual removals.
func (f *cleanupFake) Execute(_ context.Context, in liverun.Invocation) ([]byte, error) {
	f.calls = append(f.calls, in)
	key := in.Program + " " + strings.Join(in.Args, " ")
	if f.fail != "" && strings.Contains(key, f.fail) {
		return nil, fmt.Errorf("private error /secret/path token=secret")
	}
	if in.Program == "git" {
		if in.Args[0] == "status" {
			return nil, nil
		}
		if f.revision != "" {
			return []byte(f.revision), nil
		}
		return []byte(strings.Repeat("a", 40)), nil
	}
	if in.Program == "go" {
		if f.collectorFail {
			return nil, fmt.Errorf("collector private error /secret/path")
		}
		for i, arg := range in.Args {
			if arg == "-record" {
				path := filepath.Join(filepath.Dir(in.Args[i+1]), "durations-linux.json")
				if strings.Contains(in.Args[i+1], "windows-11") {
					path = filepath.Join(filepath.Dir(in.Args[i+1]), "durations-windows-11.json")
				}
				if err := os.WriteFile(path, []byte(`{"cases":[]}`), 0600); err != nil {
					return nil, err
				}
				return []byte(path + "\n"), nil
			}
		}
	}
	if in.Program == "ssh" {
		return []byte("Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/sda 1000 50 950 5% /\n"), nil
	}
	if f.unreachable {
		return nil, fmt.Errorf("connection refused")
	}
	args := in.Args
	if len(args) > 2 && args[0] == "--connection" {
		args = args[2:]
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("empty process")
	}
	switch args[0] {
	case "machine":
		if args[1] == "list" {
			return []byte(`[{"Name":"selected","Default":true,"VMType":"wsl"}]`), nil
		}
		if args[1] == "inspect" {
			return []byte(`[{"Name":"selected","State":"running","Rootful":false,"SSHConfig":{"IdentityPath":"C:\\private\\key","Port":2222,"RemoteUsername":"user"}}]`), nil
		}
	case "info":
		f.infoCalls++
		if f.disconnectAfter > 0 && f.infoCalls >= f.disconnectAfter {
			return nil, fmt.Errorf("connection refused")
		}
		engine := f.engine
		if engine == "" {
			engine = "engine"
		}
		return []byte(`{"host":{"hostname":"` + engine + `"},"store":{"graphRoot":"/private/storage"}}`), nil
	case "images":
		ids := []string{}
		for id := range f.images {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		values := []map[string]string{}
		for _, id := range ids {
			values = append(values, map[string]string{"Id": id})
		}
		return json.Marshal(values)
	case "image":
		if len(args) != 3 || args[1] != "inspect" {
			return nil, fmt.Errorf("forbidden image call")
		}
		image, ok := f.images[args[2]]
		if !ok {
			return nil, fmt.Errorf("image not found")
		}
		return json.Marshal([]cleanupFakeImage{image})
	case "ps":
		f.psCalls++
		if f.lateReference != "" && f.psCalls == 2 {
			c := liverun.Container{ID: "other-group-container", Image: f.lateReference}
			f.containers = append(f.containers, c)
		}
		values := []map[string]string{}
		for _, c := range f.containers {
			values = append(values, map[string]string{"ID": c.ID})
		}
		return json.Marshal(values)
	case "container":
		return json.Marshal(f.containers)
	case "rmi":
		if len(args) != 3 || args[1] != "--no-prune" {
			return nil, fmt.Errorf("forbidden removal")
		}
		for _, c := range f.containers {
			if c.Image == args[2] {
				return nil, fmt.Errorf("image is used")
			}
		}
		if !f.keepAfterRemoval {
			delete(f.images, args[2])
		}
		return []byte(args[2]), nil
	case "system":
		if len(args) == 2 && args[1] == "df" {
			return []byte("TYPE TOTAL ACTIVE SIZE RECLAIMABLE\nImages 1 0 123B 123B (100%)\n/private/path secret\n"), nil
		}
	}
	return nil, fmt.Errorf("unexpected process: %s", key)
}

// cleanupArtifact writes a literal private invocation artifact into the test run directory.
func cleanupArtifact(t *testing.T, directory, name string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
		t.Fatal(err)
	}
}

// cleanupHarness makes one finished invocation with a complete before inventory.
func cleanupHarness(t *testing.T) (*liverun.Service, *cleanupFake, liverun.CleanupOptions) {
	t.Helper()
	root := t.TempDir()
	run := filepath.Join(root, "private")
	if err := os.Mkdir(run, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Minute)
	options := liverun.Options{Checkout: root, RunDir: run, RecordDir: filepath.Join(root, "records"), LockRoot: filepath.Join(root, "locks"), Revision: strings.Repeat("a", 40)}
	if err := os.Mkdir(options.RecordDir, 0700); err != nil {
		t.Fatal(err)
	}
	cleanupArtifact(t, run, "options.json", options)
	cleanupArtifact(t, run, "before.json", liverun.Snapshot{Complete: true, EngineID: "engine:/private/storage", Target: liverun.Target{Storage: "/private/storage"}, Images: []liverun.Image{}, Containers: []liverun.Container{}, Disks: []liverun.Disk{{Name: "engine", Path: "/private/storage", FreeBytes: 900}}, SystemDF: "TYPE TOTAL ACTIVE SIZE RECLAIMABLE\nImages 0 0 0B 0B (0%)\n", TakenAt: now.Add(-time.Minute)})
	cleanupArtifact(t, run, "started.json", map[string]any{"PreparationStart": now.Add(-time.Minute), "PreparationEnd": now})
	cleanupArtifact(t, run, "exit.json", map[string]any{"Code": 0, "EndedAt": now.Add(time.Second), "Interrupted": false})
	if err := os.WriteFile(filepath.Join(run, "exit-status"), []byte("0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "suite.log"), []byte("asset-sha256 "+strings.Repeat("e", 64)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fake := &cleanupFake{}
	return &liverun.Service{Executor: fake, BuildEvidence: func(context.Context, string) ([]liverun.BuildResult, error) {
		results := append([]liverun.BuildResult(nil), fake.produced...)
		for i := range results {
			results[i].Asset = strings.Repeat("e", 64)
		}
		return results, nil
	}, Platform: "linux", CoordinationRoot: options.LockRoot, FreeSpace: func(string) (uint64, error) { return 950, nil }}, fake, liverun.CleanupOptions{RunDir: run}
}

// TestCleanupUnreachableEngine proves the Cleanup seam returns pending and performs no removal.
func TestCleanupUnreachableEngine(t *testing.T) {
	svc, fake, options := cleanupHarness(t)
	fake.unreachable = true
	var out bytes.Buffer
	err := svc.Cleanup(context.Background(), options, &out)
	const want = "cleanup: pending (engine unreachable: connection refused; check the selected Podman service; private diagnostic retained)"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v; want %s", err, want)
	}
	for _, call := range fake.calls {
		if strings.Contains(strings.Join(call.Args, " "), "rmi") {
			t.Fatal("removed on unreachable engine")
		}
	}
}

// cleanupImageFixture returns a full-ID image with independently specified size and asset label.
func cleanupImageFixture(id string) cleanupFakeImage {
	return cleanupFakeImage{Image: liverun.Image{ID: id, Created: time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC), Labels: map[string]string{"io.sandboxed-agents.asset-sha256": strings.Repeat("e", 64)}, RepoTags: []string{"localhost/sandboxed-agents:none-" + strings.Repeat("e", 64)}}, Size: 123}
}

// cleanupBuildLog preserves exact adjacent build-result proof in the private suite log.
func cleanupBuildLog(t *testing.T, fake *cleanupFake, run, step, id string) {
	t.Helper()
	fake.produced = nil
	if strings.Contains(step, "STEP ") && !strings.Contains(step, ": FROM ") {
		fake.produced = []liverun.BuildResult{{ID: id, Step: step, Source: "trusted fixture build producer"}}
	}
	if err := os.WriteFile(filepath.Join(run, "suite.log"), []byte("asset-sha256 "+strings.Repeat("e", 64)+"\nlive operation image build: started\n"+step+"\n--> "+id+"\nlive operation image build: finished after 1s\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

// cleanupRemoved returns only actual destructive calls from the fake process boundary.
func cleanupRemoved(fake *cleanupFake) []string {
	result := []string{}
	for _, call := range fake.calls {
		args := call.Args
		if len(args) > 2 && args[0] == "--connection" {
			args = args[2:]
		}
		if len(args) > 0 && args[0] == "rmi" {
			result = append(result, args[len(args)-1])
		}
	}
	return result
}

// TestCleanupEligibility proves each exclusion through Cleanup and the fake whole-engine boundary.
func TestCleanupEligibility(t *testing.T) {
	for _, name := range []string{"presentbefore", "missingproof", "wronghash", "emptyhash", "usedothergroup", "ambiguous", "failedquery", "retention", "chainonly", "downloaded", "cache", "arbitrary", "latecontainer"} {
		t.Run(name, func(t *testing.T) {
			svc, fake, options := cleanupHarness(t)
			id := "sha256:" + strings.Repeat("b", 64)
			image := cleanupImageFixture(id)
			fake.images = map[string]cleanupFakeImage{id: image}
			cleanupBuildLog(t, fake, options.RunDir, "STEP 2/3: LABEL example=value", id)
			switch name {
			case "presentbefore":
				var before liverun.Snapshot
				data, _ := os.ReadFile(filepath.Join(options.RunDir, "before.json"))
				_ = json.Unmarshal(data, &before)
				before.Images = []liverun.Image{image.Image}
				cleanupArtifact(t, options.RunDir, "before.json", before)
			case "missingproof", "chainonly", "arbitrary":
				fake.produced = nil
				if err := os.WriteFile(filepath.Join(options.RunDir, "suite.log"), []byte("asset-sha256 "+strings.Repeat("e", 64)+"\nrandom log contains --> "+id+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "wronghash":
				image.Labels["io.sandboxed-agents.asset-sha256"] = strings.Repeat("f", 64)
			case "emptyhash":
				image.Labels["io.sandboxed-agents.asset-sha256"] = ""
			case "usedothergroup":
				fake.containers = []liverun.Container{{ID: "other-group", Image: id}}
			case "ambiguous":
				delete(fake.images, id)
				id = "sha256:123456789abc" + strings.Repeat("a", 52)
				image = cleanupImageFixture(id)
				other := "sha256:123456789abc" + strings.Repeat("b", 52)
				fake.images[other] = cleanupImageFixture(other)
				cleanupBuildLog(t, fake, options.RunDir, "STEP 2/3: LABEL example=value", "123456789abc")
			case "failedquery":
				fake.fail = "image inspect"
			case "retention":
				options.Retain = []string{id}
			case "downloaded":
				cleanupBuildLog(t, fake, options.RunDir, "STEP 1/3: FROM registry.example/base:tag", id)
			case "cache":
				cleanupBuildLog(t, fake, options.RunDir, "--> Using cache", id)
			case "latecontainer":
				fake.lateReference = id
			}
			fake.images[id] = image
			var out bytes.Buffer
			err := svc.Cleanup(context.Background(), options, &out)
			if len(cleanupRemoved(fake)) != 0 {
				t.Fatalf("excluded image removed: %v; error %v; report %s", cleanupRemoved(fake), err, out.String())
			}
			if name == "failedquery" && (err == nil || !strings.Contains(out.String(), "unverified")) {
				t.Fatalf("failed query treated as absence: %v %s", err, out.String())
			}
		})
	}
}

// TestCleanupChildrenBeforeParents proves exact removals preserve unproven ancestors and never prune or force.
func TestCleanupChildrenBeforeParents(t *testing.T) {
	svc, fake, options := cleanupHarness(t)
	parent := "sha256:" + strings.Repeat("a", 64)
	child := "sha256:" + strings.Repeat("b", 64)
	ancestor := "sha256:" + strings.Repeat("c", 64)
	p := cleanupImageFixture(parent)
	p.Parent = ancestor
	c := cleanupImageFixture(child)
	c.Parent = parent
	c.Labels["io.sandboxed-agents.base-image"] = parent
	a := cleanupImageFixture(ancestor)
	fake.images = map[string]cleanupFakeImage{parent: p, child: c, ancestor: a}
	fake.produced = []liverun.BuildResult{{ID: parent, Step: "STEP 2/3: LABEL example=value", Source: "trusted fixture build producer"}, {ID: child, Step: "COMMIT localhost/sandboxed-agents:dotnet-" + strings.Repeat("e", 64), Source: "trusted fixture build producer"}}
	log := "asset-sha256 " + strings.Repeat("e", 64) + "\nlive operation rebuild: started\nSTEP 2/3: LABEL example=value\n--> " + parent + "\nCOMMIT localhost/sandboxed-agents:dotnet-" + strings.Repeat("e", 64) + "\n--> " + child + "\nlive operation rebuild: finished after 1s\n"
	if err := os.WriteFile(filepath.Join(options.RunDir, "suite.log"), []byte(log), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := svc.Cleanup(context.Background(), options, &out); err != nil && !strings.Contains(out.String(), `"status":"partial"`) {
		t.Fatalf("cleanup: %v; %s", err, out.String())
	}
	if got := strings.Join(cleanupRemoved(fake), ","); got != child+","+parent {
		t.Fatalf("removal order %s", got)
	}
	if _, ok := fake.images[ancestor]; !ok {
		t.Fatal("unproven ancestor removed")
	}
	for _, call := range fake.calls {
		args := strings.Join(call.Args, " ")
		for _, forbidden := range []string{"prune", "reset", "--force", "machine rm", "wsl"} {
			if strings.Contains(args, forbidden) && !(forbidden == "prune" && strings.Contains(args, "rmi --no-prune ")) {
				t.Fatalf("forbidden call: %+v", call)
			}
		}
		if len(call.Args) > 0 && call.Args[0] == "rmi" && (len(call.Args) != 3 || call.Args[1] != "--no-prune") {
			t.Fatalf("unsafe removal: %+v", call)
		}
	}
	var report liverun.CleanupReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Removed) != 2 || report.Removed[0].SizeBytes != 123 || report.Before.FreeSpace[0].FreeBytes != 900 || report.After.FreeSpace[0].FreeBytes != 950 || !report.Collected || report.PostSuiteSeconds < 59 {
		t.Fatalf("missing evidence: %+v", report)
	}
	if strings.Contains(out.String(), "private") || strings.Contains(out.String(), "secret") || strings.Contains(out.String(), "/storage") {
		t.Fatalf("private host evidence leaked: %s", out.String())
	}
	entries, err := os.ReadDir(options.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	var private string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "cleanup-") {
			private = filepath.Join(options.RunDir, entry.Name())
		}
	}
	data, err := os.ReadFile(filepath.Join(private, "build-record.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(child)) || !bytes.Contains(data, []byte("COMMIT localhost/sandboxed-agents:dotnet-")) {
		t.Fatalf("private proof missing: %s", data)
	}
	for _, name := range []string{"suite.log", "before.json", "options.json", "exit.json"} {
		if _, err := os.Stat(filepath.Join(options.RunDir, name)); err != nil {
			t.Fatal("invocation evidence deleted", name)
		}
	}
	for _, call := range fake.calls {
		if call.Program != "go" {
			continue
		}
		args := strings.Join(call.Args, " ")
		for _, flag := range []string{"-post-suite-start", "-post-suite-end", "-preparation-start", "-preparation-end"} {
			if !strings.Contains(args, flag) {
				t.Fatalf("collector omitted %s", flag)
			}
		}
	}
}

// TestCleanupUniqueShortIDAndUnlabelledStep proves a uniquely inspected step result may identify an unlabelled intermediate.
func TestCleanupUniqueShortIDAndUnlabelledStep(t *testing.T) {
	svc, fake, options := cleanupHarness(t)
	id := "sha256:123456789abc" + strings.Repeat("a", 52)
	image := cleanupImageFixture(id)
	image.Labels = nil
	fake.produced = []liverun.BuildResult{{ID: id, Step: "STEP 2/3: COPY file /path", Source: "trusted fixture build producer"}}
	fake.images = map[string]cleanupFakeImage{id: image}
	cleanupBuildLog(t, fake, options.RunDir, "[1/2] STEP 2/3: COPY file /path", "123456789abc")
	var out bytes.Buffer
	if err := svc.Cleanup(context.Background(), options, &out); err != nil {
		t.Fatalf("%v %s", err, out.String())
	}
	if got := cleanupRemoved(fake); len(got) != 1 || got[0] != id {
		t.Fatalf("short ID not uniquely resolved: %v", got)
	}
}

// TestCleanupFailedContainerAndInventoryQueries proves failed queries never establish object absence at Cleanup.
func TestCleanupFailedContainerAndInventoryQueries(t *testing.T) {
	for _, query := range []string{"images --all", "ps --all", "container inspect", "rmi --no-prune"} {
		t.Run(query, func(t *testing.T) {
			svc, fake, options := cleanupHarness(t)
			id := "sha256:" + strings.Repeat("b", 64)
			fake.images = map[string]cleanupFakeImage{id: cleanupImageFixture(id)}
			fake.fail = query
			if query == "container inspect" {
				fake.containers = []liverun.Container{{ID: "other-group", Image: id}}
			}
			cleanupBuildLog(t, fake, options.RunDir, "STEP 2/3: LABEL example=value", id)
			var out bytes.Buffer
			err := svc.Cleanup(context.Background(), options, &out)
			if err == nil || !strings.Contains(out.String(), "unverified") {
				t.Fatalf("failed query not unverified: %v %s", err, out.String())
			}
			if query != "rmi --no-prune" && len(cleanupRemoved(fake)) != 0 {
				t.Fatal("failed inventory allowed removal")
			}
			if strings.Contains(out.String(), "secret") {
				t.Fatal("query diagnostics leaked")
			}
		})
	}
}

// TestCleanupRefusesInvalidInvocation proves changed and incomplete evidence stops removal at Cleanup.
func TestCleanupRefusesInvalidInvocation(t *testing.T) {
	for _, name := range []string{"incompletebefore", "invalidbeforeid", "missingbeforefields", "changedengine", "changedcheckout", "missingexit", "statusmismatch", "missingstart", "oversizedlog"} {
		t.Run(name, func(t *testing.T) {
			svc, fake, options := cleanupHarness(t)
			id := "sha256:" + strings.Repeat("b", 64)
			fake.images = map[string]cleanupFakeImage{id: cleanupImageFixture(id)}
			cleanupBuildLog(t, fake, options.RunDir, "STEP 2/3: LABEL example=value", id)
			switch name {
			case "incompletebefore", "invalidbeforeid":
				var before liverun.Snapshot
				data, _ := os.ReadFile(filepath.Join(options.RunDir, "before.json"))
				_ = json.Unmarshal(data, &before)
				if name == "incompletebefore" {
					before.Complete = false
				} else {
					before.Images = []liverun.Image{{ID: "partial"}}
				}
				cleanupArtifact(t, options.RunDir, "before.json", before)
			case "missingbeforefields":
				cleanupArtifact(t, options.RunDir, "before.json", map[string]any{"Complete": true, "EngineID": "engine:/private/storage"})
			case "changedengine":
				fake.engine = "different"
			case "changedcheckout":
				fake.revision = strings.Repeat("f", 40)
			case "missingexit":
				if err := os.Remove(filepath.Join(options.RunDir, "exit.json")); err != nil {
					t.Fatal(err)
				}
			case "statusmismatch":
				if err := os.WriteFile(filepath.Join(options.RunDir, "exit-status"), []byte("1\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missingstart":
				if err := os.Remove(filepath.Join(options.RunDir, "started.json")); err != nil {
					t.Fatal(err)
				}
			case "oversizedlog":
				if err := os.Truncate(filepath.Join(options.RunDir, "suite.log"), 64*1024*1024+1); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			if err := svc.Cleanup(context.Background(), options, &out); err == nil {
				t.Fatal("invalid invocation accepted")
			}
			if len(cleanupRemoved(fake)) != 0 {
				t.Fatal("removed with invalid invocation")
			}
		})
	}
}

// TestCleanupRetentionAndFailedCollection proves retained byte cost and collector failure remain visible without reusing old evidence.
func TestCleanupRetentionAndFailedCollection(t *testing.T) {
	svc, fake, options := cleanupHarness(t)
	id := "sha256:" + strings.Repeat("b", 64)
	fake.images = map[string]cleanupFakeImage{id: cleanupImageFixture(id)}
	options.Retain = []string{id}
	fake.collectorFail = true
	cleanupBuildLog(t, fake, options.RunDir, "STEP 2/3: LABEL example=value", id)
	path := filepath.Join(filepath.Dir(options.RunDir), "records", "durations-linux.json")
	if err := os.WriteFile(path, []byte("previous"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := svc.Cleanup(context.Background(), options, &out); err == nil {
		t.Fatal("collector failure hidden")
	}
	var report liverun.CleanupReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].Reason != "retained by user" || report.Skipped[0].SizeBytes != 123 || report.Collected || report.DurationsFile != "" {
		t.Fatalf("invalid evidence: %+v", report)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "previous" {
		t.Fatal("old evidence changed")
	}
}

// TestParseCleanupArgs proves the dispatch parser admits only a run directory and full retention IDs.
func TestParseCleanupArgs(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	parsed, err := liverun.ParseCleanupArgs([]string{"-run-dir", "relative", "-retain", id}, &bytes.Buffer{})
	if err != nil || !filepath.IsAbs(parsed.RunDir) || len(parsed.Retain) != 1 || parsed.Retain[0] != id {
		t.Fatalf("parser: %+v %v", parsed, err)
	}
	for _, args := range [][]string{{}, {"-run-dir", "a", "extra"}, {"-run-dir", "a", "-retain", "123456789abc"}, {"-run-dir", "a", "-retain", "--force"}, {"-run-dir", "a", "-force"}} {
		if _, err := liverun.ParseCleanupArgs(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
	}
}

// TestCleanupDispatch proves the public cleanup phase delegates validated arguments to the same safe command seam.
func TestCleanupDispatch(t *testing.T) {
	svc, fake, options := cleanupHarness(t)
	fake.images = map[string]cleanupFakeImage{}
	var out bytes.Buffer
	if err := svc.RunDispatch(context.Background(), []string{"cleanup", "-run-dir", options.RunDir}, &out, &out); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(out.String(), `"collected":true`) {
		t.Fatalf("missing final evidence: %s", out.String())
	}
}

// TestCleanupFinalReconciliationDetectsFalseSuccess proves removal output alone never certifies an image is gone.
func TestCleanupFinalReconciliationDetectsFalseSuccess(t *testing.T) {
	svc, fake, options := cleanupHarness(t)
	id := "sha256:" + strings.Repeat("b", 64)
	fake.images = map[string]cleanupFakeImage{id: cleanupImageFixture(id)}
	fake.keepAfterRemoval = true
	cleanupBuildLog(t, fake, options.RunDir, "STEP 2/3: LABEL example=value", id)
	var out bytes.Buffer
	if err := svc.Cleanup(context.Background(), options, &out); err == nil || !strings.Contains(out.String(), "removal output disagrees") {
		t.Fatalf("false removal success certified: %v %s", err, out.String())
	}
}

// TestCleanupDisconnectBeforeInventory proves a newly unreachable engine stops with the exact pending form and no removal.
func TestCleanupDisconnectBeforeInventory(t *testing.T) {
	svc, fake, options := cleanupHarness(t)
	fake.disconnectAfter = 2
	var out bytes.Buffer
	err := svc.Cleanup(context.Background(), options, &out)
	const want = "cleanup: pending (engine unreachable: connection refused; check the selected Podman service; private diagnostic retained)"
	if err == nil || err.Error() != want || len(cleanupRemoved(fake)) != 0 {
		t.Fatalf("got %v; removed %v; want %s", err, cleanupRemoved(fake), want)
	}
}

// TestCleanupUnlabelledProofNeedsNoAssetLabel proves exact build provenance suffices for an unlabelled intermediate.
func TestCleanupUnlabelledProofNeedsNoAssetLabel(t *testing.T) {
	svc, fake, options := cleanupHarness(t)
	id := "sha256:" + strings.Repeat("b", 64)
	image := cleanupImageFixture(id)
	image.Labels = nil
	fake.produced = []liverun.BuildResult{{ID: id, Step: "STEP 2/3: COPY file /path", Source: "trusted fixture build producer"}}
	fake.images = map[string]cleanupFakeImage{id: image}
	if err := os.WriteFile(filepath.Join(options.RunDir, "suite.log"), []byte("live operation image build: started\nSTEP 2/3: COPY file /path\n--> "+id+"\nlive operation image build: finished after 1s\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := svc.Cleanup(context.Background(), options, &out); err != nil {
		t.Fatalf("%v %s", err, out.String())
	}
	if len(cleanupRemoved(fake)) != 1 {
		t.Fatalf("proven unlabelled intermediate skipped: %s", out.String())
	}
}

// TestCleanupWindowsUsesSelectedConnection proves cleanup queries, removals and both filesystem measurements stay on the selected fake engine.
func TestCleanupWindowsUsesSelectedConnection(t *testing.T) {
	svc, fake, options := cleanupHarness(t)
	svc.Platform = "windows"
	svc.MachineDisk = func(string) (string, error) { return `C:\private\ext4.vhdx`, nil }
	data, err := os.ReadFile(filepath.Join(options.RunDir, "before.json"))
	if err != nil {
		t.Fatal(err)
	}
	var before map[string]any
	if err = json.Unmarshal(data, &before); err != nil {
		t.Fatal(err)
	}
	before["Target"] = map[string]any{"Connection": "selected", "Storage": "/private/storage", "MachineDiskPath": `C:\private\ext4.vhdx`, "SSH": map[string]any{"IdentityPath": `C:\private\key`, "Port": 2222, "RemoteUsername": "user"}}
	before["Disks"] = []map[string]any{{"Name": "host", "Path": `C:\private\ext4.vhdx`, "FreeBytes": 900}, {"Name": "engine", "Path": "/private/storage", "FreeBytes": 900}}
	cleanupArtifact(t, options.RunDir, "before.json", before)
	id := "sha256:" + strings.Repeat("b", 64)
	fake.images = map[string]cleanupFakeImage{id: cleanupImageFixture(id)}
	cleanupBuildLog(t, fake, options.RunDir, "STEP 2/3: LABEL example=value", id)
	var out bytes.Buffer
	if err = svc.Cleanup(context.Background(), options, &out); err != nil {
		t.Fatalf("%v %s", err, out.String())
	}
	for _, call := range fake.calls {
		if call.Program == "podman" && call.Args[0] != "machine" && (len(call.Args) < 3 || call.Args[0] != "--connection" || call.Args[1] != "selected") {
			t.Fatalf("unbound call: %+v", call)
		}
	}
	var report liverun.CleanupReport
	if err = json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Before.FreeSpace) != 2 || len(report.After.FreeSpace) != 2 || report.DurationsFile != "durations-windows-11.json" {
		t.Fatalf("Windows evidence: %+v", report)
	}
}
