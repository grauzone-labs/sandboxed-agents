package main_test

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/image"
	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

const (
	tracerGroup      = "live-offline-tracer"
	tracerPort       = "2300"
	tracerHostKey    = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAB"
	tracerAuthorized = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

var tracerAssetHash = strings.Repeat("a", 64)

// tracerImageID names a deterministic image ID per toolchain set and generation.
func tracerImageID(set string, generation int) string {
	seed := fmt.Sprintf("%x", set+"-"+strconv.Itoa(generation))
	return "sha256:" + strings.Repeat(seed, 64/len(seed)+1)[:64]
}

// tracerTag names the image tag of a toolchain set under the tracer's asset hash.
func tracerTag(set string) string {
	if set == "none" {
		return image.Tag(tracerAssetHash, nil)
	}
	return image.Tag(tracerAssetHash, []string{set})
}

// tracerContainer renders Podman's inspect answer for a running sandbox on the given image.
func tracerContainer(set string, generation int) string {
	record := map[string]any{
		"Image": tracerImageID(set, generation),
		"State": map[string]any{"Running": true},
		"Config": map[string]any{"Image": tracerTag(set), "User": "0:0", "Labels": map[string]string{
			cli.OwnerLabel: tracerGroup, cli.SSHPortLabel: tracerPort, cli.ToolchainsLabel: set,
		}},
		"HostConfig": map[string]any{"Memory": 8589934592, "MemorySwap": 8589934592, "PortBindings": map[string]any{"22/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": tracerPort}}}},
	}
	data, _ := json.Marshal([]any{record})
	return string(data)
}

// tracerToolContainer supplies the ADR-0009 containment fields the Tool case observes on live Podman.
func tracerToolContainer(t *testing.T, set string) string {
	t.Helper()
	var records []map[string]any
	if err := json.Unmarshal([]byte(tracerContainer(set, 1)), &records); err != nil {
		t.Fatal(err)
	}
	record := records[0]
	record["Config"].(map[string]any)["CreateCommand"] = []string{"podman", "create", "--userns=keep-id:uid=1000,gid=1000", "--user=0:0", "--security-opt=no-new-privileges", "--network=pasta:--no-map-gw"}
	record["HostConfig"].(map[string]any)["NetworkMode"] = "pasta"
	names := cli.DerivedNames(tracerGroup, "live-t3code")
	var mounts []map[string]string
	for destination, name := range map[string]string{"/workspace": names.Workspace, "/home/agent": names.Home, "/etc/ssh": names.SSH} {
		mounts = append(mounts, map[string]string{"Type": "volume", "Name": name, "Source": "/home/test/.local/share/containers/storage/volumes/" + name + "/_data", "Destination": destination})
	}
	slices.SortFunc(mounts, func(a, b map[string]string) int { return strings.Compare(a["Destination"], b["Destination"]) })
	options := "rw,rprivate,nosuid,nodev,rbind"
	if set == "native" {
		// Podman's named-volume iteration can reorder equivalent mounts and options on replacement.
		slices.Reverse(mounts)
		options = "rbind,nodev,nosuid,rprivate,rw"
	}
	record["Mounts"] = mounts
	var binds []string
	for _, mount := range mounts {
		binds = append(binds, mount["Name"]+":"+mount["Destination"]+":"+options)
	}
	record["HostConfig"].(map[string]any)["Binds"] = binds
	data, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// tracerToolSelection is one scripted installed version and its channel or pin.
type tracerToolSelection struct{ version, channel, pin string }

// tracerToolResponses scripts distinct channel versions, a pinned version, and the deterministic failed staging.
func tracerToolResponses(t *testing.T, executableResponses, podmanResponses map[string]lifecycleResponse, failIdentity bool) {
	t.Helper()
	const name = "live-t3code"
	var statuses, probes []scriptedAnswer
	selections := []tracerToolSelection{
		{"0.17.0", "latest", ""}, {"0.17.1", "latest", ""},
		{"0.18.0-nightly.1", "nightly", ""}, {"0.18.0-nightly.2", "nightly", ""},
	}
	for range 8 {
		selections = append(selections, tracerToolSelection{"0.17.1", "", "0.17.1"})
	}
	selections = append(selections, tracerToolSelection{"0.17.2", "latest", ""})
	for index, selection := range selections {
		channel, pin := selection.channel, selection.pin
		if channel == "" {
			channel = "none"
		}
		if pin == "" {
			pin = "none"
		}
		statuses = append(statuses, scriptedAnswer{Output: fmt.Sprintf("t3code: enabled; version %s; channel %s; pin %s; service running; log /private/service.log\n", selection.version, channel, pin)})
		uid := 1000
		if failIdentity && index == 0 {
			uid = 1001
		}
		probe, err := json.Marshal(liveToolObservation{Version: selection.version, Channel: selection.channel, Pin: selection.pin, UID: uid, GID: 1000, SupervisorPID: 200, ServicePID: 201, PIDs: []int{200, 201, 202}, Origin: "http://127.0.0.1:3773", HTTPStatus: 200})
		if err != nil {
			t.Fatal(err)
		}
		probes = append(probes, scriptedAnswer{Output: string(probe)})
	}
	// Deletion occurs before the final fresh enable.
	last := statuses[len(statuses)-1]
	statuses = append(statuses[:len(statuses)-1], scriptedAnswer{Output: "t3code: not enabled\n"}, scriptedAnswer{Output: "t3code: not enabled\n"}, last)
	executableResponses["tools status "+name+" t3code"] = lifecycleResponse{Sequence: statuses}
	executableResponses["tools update "+name+" t3code"] = lifecycleResponse{Sequence: []scriptedAnswer{{}, {}, {}, {Exit: 1, Output: "tools update t3code: mkdir npm/.next-0.17.1: permission denied\n"}}}
	for _, command := range []string{"up " + name, "stop " + name, "start " + name, "restart " + name, "check " + name, "tools enable " + name + " t3code", "tools enable " + name + " t3code --channel nightly", "tools enable " + name + " t3code --version 0.17.1", "tools delete " + name + " t3code"} {
		executableResponses[command] = lifecycleResponse{}
	}
	executableResponses["tools delete "+name+" t3code"] = lifecycleResponse{Sequence: []scriptedAnswer{{Output: "t3code deleted\n"}, {Output: "t3code not enabled; nothing to delete\n"}}}
	executableResponses["update "+name+" --with native"] = lifecycleResponse{Output: "sandbox live-t3code updated\n"}
	executableResponses["update "+name+" --with none --force"] = lifecycleResponse{Exit: 1, Output: "readiness wait failed: restored sandbox live-t3code to its previous state\n"}
	var containers []scriptedAnswer
	for range 11 {
		containers = append(containers, scriptedAnswer{Output: tracerToolContainer(t, "none")})
	}
	for range 5 {
		containers = append(containers, scriptedAnswer{Output: tracerToolContainer(t, "native")})
	}
	podmanResponses["container inspect "+cli.DerivedNames(tracerGroup, name).Container] = lifecycleResponse{Sequence: containers}
	probePrefix := "exec --user=1000:1000 --env HOME=/home/agent * node -e * "
	podmanResponses[probePrefix+"running"] = lifecycleResponse{Sequence: probes}
	podmanResponses[probePrefix+"break-install"] = lifecycleResponse{}
	podmanResponses[probePrefix+"repair-install"] = lifecycleResponse{}
	podmanResponses[probePrefix+"deleted *"] = lifecycleResponse{Output: `{"deleted":true}`}
}

// tracerToolBindFailure renders an inspect response with one forbidden bind while leaving the independent Mounts observation valid.
func tracerToolBindFailure(t *testing.T, kind string) string {
	t.Helper()
	var records []map[string]any
	if err := json.Unmarshal([]byte(tracerToolContainer(t, "none")), &records); err != nil {
		t.Fatal(err)
	}
	hostConfig := records[0]["HostConfig"].(map[string]any)
	binds := hostConfig["Binds"].([]any)
	switch kind {
	case "extra-volume":
		binds = append(binds, "unexpected-volume:/extra:rw,rprivate")
	case "host-bind":
		binds[0] = "/host/private:/etc/ssh:rw,rprivate"
	case "unsafe-options":
		binds[0] = binds[0].(string) + ",rshared"
	default:
		t.Fatalf("unknown bind failure %q", kind)
	}
	hostConfig["Binds"] = binds
	data, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// tracerImage renders Podman's image inspect answer with the suite's labels and shared base layers.
func tracerImage(set string, generation int) string {
	labels := map[string]string{"io.sandboxed-agents.image": "sandboxed-agents", "io.sandboxed-agents.asset-sha256": tracerAssetHash, "io.sandboxed-agents.toolchains": set}
	layers := []string{"sha256:base-layer-" + strconv.Itoa(generation)}
	if set != "none" {
		labels["io.sandboxed-agents.base-image"] = tracerImageID("none", generation)
		layers = append(layers, "sha256:"+set+"-layer-"+strconv.Itoa(generation))
	}
	data, _ := json.Marshal([]any{map[string]any{"Id": tracerImageID(set, generation), "Config": map[string]any{"Labels": labels}, "RootFS": map[string]any{"Layers": layers}}})
	return string(data)
}

// tracerArchive renders a docker-archive with one layer that holds no SSH host key.
func tracerArchive(t *testing.T) string {
	t.Helper()
	var layer bytes.Buffer
	inner := tar.NewWriter(&layer)
	content := []byte("live tracer\n")
	if err := inner.WriteHeader(&tar.Header{Name: "etc/motd", Mode: 0644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	inner.Write(content)
	inner.Close()
	var archive bytes.Buffer
	outer := tar.NewWriter(&archive)
	if err := outer.WriteHeader(&tar.Header{Name: "layer.tar", Mode: 0644, Size: int64(layer.Len()), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	outer.Write(layer.Bytes())
	outer.Close()
	return archive.String()
}

// tracerListing renders list output with one row per sandbox and state.
func tracerListing(rows ...string) string {
	return "NAME\tSTATE\tSSH PORT\tWORKSPACE\tTOOLCHAINS\tAGENTS\tVOLUMES\tPROBLEM\n" + strings.Join(rows, "")
}

// TestLiveSuiteTracesEveryCaseAgainstFakes proves the whole live suite runs every case through the nested test process on a Linux pseudo-terminal, records identity and bind failures, builds exactly once only when images are selected, and removes every created sandbox using a scripted executable, fake Podman, and fake ssh.
func TestLiveSuiteTracesEveryCaseAgainstFakes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the tracer's detach keys need the Linux raw pseudo-terminal; Windows runs the terminal check alone")
	}
	if os.Getenv(nestedGuardEnv) == strconv.Itoa(os.Getppid()) {
		t.Skip("nested offline guard; the outer suite runs the tracer")
	}
	sets := []string{"none", "dotnet", "playwright", "azure", "native"}
	for _, scenario := range []struct {
		images, failIdentity bool
		bindFailure          string
	}{{false, false, ""}, {true, false, ""}, {false, true, ""}, {false, false, "extra-volume"}, {false, false, "host-bind"}, {false, false, "unsafe-options"}} {
		selected := scenario.images
		failed := scenario.failIdentity || scenario.bindFailure != ""
		label := "images-selected-" + strconv.FormatBool(selected) + "-tool-identity-failure-" + strconv.FormatBool(scenario.failIdentity)
		if scenario.bindFailure != "" {
			label += "-" + scenario.bindFailure
		}
		t.Run(label, func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			data, err := os.ReadFile(fakeExecutable)
			if err != nil {
				t.Fatal(err)
			}
			// The scripted executable lives outside the fake PATH directory, which
			// also serves as the host state root of the nested run.
			scripted := filepath.Join(t.TempDir(), "sandboxed-agents")
			if err := os.WriteFile(scripted, data, 0700); err != nil {
				t.Fatal(err)
			}
			running := func(name, set string) string {
				return name + "\trunning\t" + tracerPort + "\tvolume\t" + set + "\t-\t3\t-\n"
			}
			var outdated []string
			for _, set := range sets {
				outdated = append(outdated, "image-"+set+"\trunning\t"+tracerPort+"\tvolume\t"+set+"\t-\t3\toutdated; use update image-"+set+"\n")
			}
			executableResponses := map[string]lifecycleResponse{
				"check":   {},
				"version": {Output: "sandboxed-agents development\nasset-sha256 " + tracerAssetHash + "\n"},
				"list": {Sequence: []scriptedAnswer{
					{Output: tracerListing(running("live-lifecycle", "none"))}, {Output: tracerListing("live-lifecycle\tstopped\t" + tracerPort + "\tvolume\tnone\t-\t3\t-\n")},
					{Output: tracerListing(running("live-lifecycle", "none"))}, {Output: tracerListing()},
					{Output: tracerListing(running("live-update", "none"))}, {Output: tracerListing(running("live-update", "none"))},
					{Output: tracerListing(outdated...)},
				}},
				"up live-lifecycle": {}, "check live-lifecycle": {}, "stop live-lifecycle": {}, "start live-lifecycle": {},
				"stop image-none": {}, "start image-none": {},
				"up live-update --with native":             {},
				"update live-update --with none":           {Output: "sandbox live-update updated\n"},
				"update live-update --with native --force": {Exit: 1, Output: "readiness wait failed: no SSH key exchange; restored sandbox live-update to its previous state\n"},
				"check live-update":                        {},
				"shell live-update":                        {Output: tracerAuthorized + "\n"},
				"up live-agent":                            {},
				"agents enable live-agent opencode":        {},
				"agents status live-agent opencode": {Sequence: []scriptedAnswer{
					{Output: "opencode: enabled; version 1.2.3; pin none; session false; signed in unknown\n"},
					{Output: "opencode: enabled; version 1.2.3; pin none; session true; signed in unknown\n"},
					{Output: "opencode: enabled; version 1.2.3; pin none; session false; signed in unknown\n"},
					{Output: "opencode: not enabled\n"},
				}},
				"agents session live-agent opencode":        {Output: "opencode session screen\n", ExitOnInput: "\x02d"},
				"agents session live-agent opencode --stop": {Output: "stopped session opencode\n"},
				"agents disable live-agent opencode":        {},
				"build":                                     {Output: "built image " + tracerTag("none") + "\nexisting sandboxes keep their image until update; list marks them as outdated\n"},
			}
			podmanResponses := map[string]lifecycleResponse{
				"* exists *":                        {Exit: 1},
				"exec --user=0:0 * ssh-host-key":    {Output: tracerHostKey + "\n"},
				"exec --user=0:0 * ssh-authorize *": {},
				"exec --user=0:0 * sh -c cat /proc/1/comm; cat /proc/1/status":                   {Output: "sshd\nName:\tsshd\nUid:\t0\t0\t0\t0\n"},
				"exec --user=1000:1000 * cat /sys/fs/cgroup/memory.swap.max":                     {Output: "0\n"},
				"ps --all --format {{.Names}}":                                                   {},
				"volume ls --format {{.Name}}":                                                   {},
				"container inspect " + cli.DerivedNames(tracerGroup, "live-lifecycle").Container: {Output: tracerContainer("none", 1)},
				"container inspect " + cli.DerivedNames(tracerGroup, "live-update").Container: {Sequence: []scriptedAnswer{
					{Output: tracerContainer("native", 1)}, {Output: tracerContainer("none", 1)}, {Output: tracerContainer("none", 1)},
				}},
				"image save --format docker-archive " + tracerTag("none"): {Output: tracerArchive(t)},
			}
			tracerToolResponses(t, executableResponses, podmanResponses, scenario.failIdentity)
			if scenario.bindFailure != "" {
				podmanResponses["container inspect "+cli.DerivedNames(tracerGroup, "live-t3code").Container] = lifecycleResponse{Output: tracerToolBindFailure(t, scenario.bindFailure)}
			}
			for _, name := range []string{"live-lifecycle", "live-update", "live-agent", "live-t3code"} {
				executableResponses["remove "+name+" --volumes"] = lifecycleResponse{}
				executableResponses["remove "+name+" --volumes --force"] = lifecycleResponse{}
			}
			for _, set := range sets {
				executableResponses["up image-"+set+" --with "+set] = lifecycleResponse{}
				executableResponses["shell image-"+set] = lifecycleResponse{}
				executableResponses["remove image-"+set+" --volumes"] = lifecycleResponse{}
				executableResponses["remove image-"+set+" --volumes --force"] = lifecycleResponse{}
				podmanResponses["container inspect "+cli.DerivedNames(tracerGroup, "image-"+set).Container] = lifecycleResponse{Output: tracerContainer(set, 1)}
				podmanResponses["image inspect "+tracerTag(set)] = lifecycleResponse{Sequence: []scriptedAnswer{{Output: tracerImage(set, 1)}, {Output: tracerImage(set, 2)}}}
			}
			executableScript, err := json.Marshal(executableResponses)
			if err != nil {
				t.Fatal(err)
			}
			podmanScript, err := json.Marshal(podmanResponses)
			if err != nil {
				t.Fatal(err)
			}
			records := t.TempDir()
			executableLog := filepath.Join(fakes.Dir, "sandboxed-agents.jsonl")
			output, err := liveSuiteChild(t, fakes, map[string]string{
				"SANDBOXED_AGENTS_GROUP": tracerGroup, liveRecordDirEnv: records, liveImagesEnv: map[bool]string{false: "", true: "1"}[selected],
				"TEST_SANDBOXED_AGENTS_EXECUTABLE": scripted, livePreparedExecutableEnv: scripted,
				"TEST_SANDBOXED_AGENTS_LOG": executableLog, "TEST_SANDBOXED_AGENTS_RESPONSES": string(executableScript),
				"TEST_PODMAN_RESPONSES": string(podmanScript),
				"TEST_SSH_OUTPUT":       "debug1: SSH2_MSG_NEWKEYS received\n", "TEST_SSH_EXIT": "255",
				"TEST_SSH_KNOWN_HOSTS": "[127.0.0.1]:" + tracerPort + " " + tracerHostKey + "\n",
			})
			failure := "disagrees with CLI status or containment"
			if scenario.bindFailure != "" {
				failure = "unexpected HostConfig.Binds"
			}
			if (!failed && (err != nil || !strings.Contains(output, "--- PASS: TestLiveSuite"))) || (failed && (err == nil || !strings.Contains(output, "--- FAIL: TestLiveSuite/t3code") || !strings.Contains(output, failure))) {
				t.Fatalf("traced suite: %v\n%s", err, output)
			}
			data, err = os.ReadFile(filepath.Join(records, liveRecordFilename(t)))
			if err != nil {
				t.Fatal(err)
			}
			var record struct {
				Result                string `json:"result"`
				ImagePartSelected     bool   `json:"image_part_selected"`
				ImagePartRan          bool   `json:"image_part_ran"`
				ImageCoverageComplete bool   `json:"image_coverage_complete"`
				Checks                []struct{ Name, Result string }
			}
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			images := map[bool]string{false: "not-run", true: "pass"}[selected]
			result := map[bool]string{false: "pass", true: "fail"}[failed]
			wantChecks := []struct{ Name, Result string }{{"preflight", "pass"}, {"lifecycle", "pass"}, {"update", "pass"}, {"agent", "pass"}, {"t3code", result}, {"images", images}}
			if record.Result != result || record.ImagePartSelected != selected || record.ImagePartRan != selected || record.ImageCoverageComplete != selected || !slices.Equal(record.Checks, wantChecks) {
				t.Fatalf("record=%s; want %s with image fields %v and checks %v", data, result, selected, wantChecks)
			}
			assertTracedToolEvidence(t, records, result, failed)
			builds, created := 0, map[string]bool{}
			commands := map[string]int{}
			for _, call := range testutil.Invocations(t, executableLog) {
				commands[strings.Join(call, " ")]++
				if slices.Equal(call, []string{"build"}) {
					builds++
				}
				if len(call) >= 2 && call[0] == "up" {
					created[call[1]] = true
				}
				if len(call) >= 3 && call[0] == "remove" && call[2] == "--volumes" {
					delete(created, call[1])
				}
			}
			if builds != map[bool]int{false: 0, true: 1}[selected] || len(created) != 0 {
				t.Fatalf("builds=%d sandboxes without a remove=%v; want %d builds and none left", builds, created, map[bool]int{false: 0, true: 1}[selected])
			}
			if !failed {
				for command, want := range map[string]int{
					"up live-t3code": 2, "stop live-t3code": 2, "start live-t3code": 1, "restart live-t3code": 1,
					"tools enable live-t3code t3code": 2, "tools enable live-t3code t3code --channel nightly": 1,
					"tools enable live-t3code t3code --version 0.17.1": 1, "tools update live-t3code t3code": 4,
					"update live-t3code --with native": 1, "update live-t3code --with none --force": 1,
					"tools delete live-t3code t3code": 2, "tools status live-t3code t3code": 15,
				} {
					if commands[command] != want {
						t.Fatalf("traced %q %d times; want %d", command, commands[command], want)
					}
				}
			}
			probes := map[string]int{}
			for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
				if slices.Contains(call, "build") || slices.Contains(call, "create") || slices.Contains(call, "rm") {
					t.Fatalf("the suite changed Podman objects directly: %v", call)
				}
				if index := slices.Index(call, "-e"); index >= 0 && len(call) > index+2 && slices.Contains(call, "node") {
					probes[call[index+2]]++
				}
			}
			wantProbes := map[string]int{"running": 13, "break-install": 1, "repair-install": 1, "deleted": 2}
			if scenario.failIdentity {
				wantProbes = map[string]int{"running": 1}
			}
			if scenario.bindFailure != "" {
				wantProbes = map[string]int{}
			}
			if !reflect.DeepEqual(probes, wantProbes) {
				t.Fatalf("Tool probes=%v; want %v", probes, wantProbes)
			}
		})
	}
}

// assertTracedToolEvidence verifies the supplemental artifact's exact fields, versions, checkpoints, and failure result at the nested suite's file boundary.
func assertTracedToolEvidence(t *testing.T, directory, result string, failed bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, "t3code-"+livePlatform()+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence liveToolEvidence
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil {
		t.Fatalf("unexpected supplemental evidence fields: %v\n%s", err, data)
	}
	commit, err := liveCommit("../..")
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Commit != commit || evidence.Platform != livePlatform() || evidence.Result != result {
		t.Fatalf("wrong Tool evidence provenance or result: %s", data)
	}
	if failed {
		if len(evidence.Observations) != 0 {
			t.Fatalf("failed containment or identity check counted as passing evidence: %s", data)
		}
		return
	}
	steps := []string{"default-enable-terminal-closed", "latest-update", "nightly-enable", "nightly-update", "pin-enable", "pin-update", "failed-update-retained", "start-restored", "restart-restored", "up-restored", "sandbox-update-restored", "sandbox-rollback-restored", "reenable-default"}
	versions := []string{"0.17.0", "0.17.1", "0.18.0-nightly.1", "0.18.0-nightly.2", "0.17.1", "0.17.1", "0.17.1", "0.17.1", "0.17.1", "0.17.1", "0.17.1", "0.17.1", "0.17.2"}
	if len(evidence.Observations) != len(steps) {
		t.Fatalf("Tool observations=%d; want %d: %s", len(evidence.Observations), len(steps), data)
	}
	for index, observation := range evidence.Observations {
		channel, pin := "", "0.17.1"
		switch index {
		case 0, 1, 12:
			channel, pin = "latest", ""
		case 2, 3:
			channel, pin = "nightly", ""
		}
		want := liveToolCheckpoint{Step: steps[index], Version: versions[index], Channel: channel, Pin: pin, UID: 1000, GID: 1000}
		if observation != want {
			t.Fatalf("Tool checkpoint %d=%+v; want %+v", index, observation, want)
		}
	}
}
