package cli_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/image"
)

func baseTag(t *testing.T) string {
	t.Helper()
	hash, err := image.Hash(image.Context())
	if err != nil {
		t.Fatal(err)
	}
	return image.Tag(hash, nil)
}

// TestUpBuildsAbsentImage proves up builds a missing image without prior build and reaches the deferred creation boundary through RunWithHost and fake Podman.
func TestUpBuildsAbsentImage(t *testing.T) {
	f := linuxHost(t)
	tag := baseTag(t)
	f.responses["container exists sandboxed-agents.default.demo"] = response{Exit: 1}
	f.responses["image exists "+tag] = response{Exit: 1}
	f.responses["build *"] = response{}
	status, output, calls := f.run(t, "up", "demo")
	if status != 1 || !strings.Contains(output, "sandbox creation is not implemented (#14)") || !strings.Contains(output, tag) {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	buildIndex := -1
	for i, call := range calls {
		if len(call) > 0 && call[0] == "build" {
			buildIndex = i
		}
	}
	if buildIndex < 0 {
		t.Fatalf("missing build: %v", calls)
	}
	if !reflect.DeepEqual(calls[buildIndex-1], []string{"image", "exists", tag}) {
		t.Fatalf("wrong build order: %v", calls)
	}
	for _, call := range calls {
		if len(call) > 0 && (call[0] == "create" || call[0] == "run" || call[0] == "volume") {
			t.Fatalf("#14 mutation: %v", calls)
		}
	}
}

// TestBuildRetainsExistingSandboxes proves explicit build uses the shared image builder and never queries or replaces old sandboxes through RunWithHost and fake Podman.
func TestBuildRetainsExistingSandboxes(t *testing.T) {
	f := linuxHost(t)
	f.responses["build *"] = response{}
	status, output, calls := f.run(t, "build")
	if status != 0 || !strings.Contains(output, baseTag(t)) {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	if len(calls) != 3 || len(calls[2]) < 1 || calls[2][0] != "build" {
		t.Fatalf("build touched sandbox: %v", calls)
	}
}

// TestUpUsesPresentImage proves up skips build for an existing shared image through RunWithHost and fake Podman.
func TestUpUsesPresentImage(t *testing.T) {
	f := linuxHost(t)
	tag := baseTag(t)
	f.responses["container exists sandboxed-agents.default.demo"] = response{Exit: 1}
	f.responses["image exists "+tag] = response{}
	status, output, calls := f.run(t, "up", "demo")
	if status != 1 || !strings.Contains(output, "sandbox creation is not implemented (#14)") || !strings.Contains(output, tag) {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	noMutation(t, calls)
}

// TestUpExistingSandboxDoesNotRebuild proves existing sandboxes bypass image inspection and rebuild, preserving their image at the exported CLI seam.
func TestUpExistingSandboxDoesNotRebuild(t *testing.T) {
	f := linuxHost(t)
	f.responses["container exists sandboxed-agents.default.demo"] = response{}
	status, output, calls := f.run(t, "up", "demo")
	if status != 1 || !strings.Contains(output, "existing sandbox lifecycle is not implemented (#14)") {
		t.Fatalf("status=%d output=%q", status, output)
	}
	if len(calls) != 3 || !reflect.DeepEqual(calls[2], []string{"container", "exists", "sandboxed-agents.default.demo"}) {
		t.Fatalf("image inspected or changed: %v", calls)
	}
	noMutation(t, calls)
}

// TestImageQueryFailuresDoNotBuild proves only exit status 1 means absence and other image or container query failures cannot trigger builds through RunWithHost.
func TestImageQueryFailuresDoNotBuild(t *testing.T) {
	for _, kind := range []string{"container", "image"} {
		t.Run(kind, func(t *testing.T) {
			f := linuxHost(t)
			f.responses["container exists sandboxed-agents.default.demo"] = response{Exit: 1}
			key := "container exists sandboxed-agents.default.demo"
			if kind == "image" {
				key = "image exists " + baseTag(t)
			}
			f.responses[key] = response{Output: "query unavailable", Exit: 125}
			status, output, calls := f.run(t, "up", "demo")
			if status != 1 || !strings.Contains(output, "query unavailable") {
				t.Fatalf("status=%d output=%q", status, output)
			}
			noMutation(t, calls)
		})
	}
}

// TestImageBuildFailureStopsUp proves a failed image build prevents new sandbox preparation through RunWithHost and fake Podman.
func TestImageBuildFailureStopsUp(t *testing.T) {
	f := linuxHost(t)
	f.responses["container exists sandboxed-agents.default.demo"] = response{Exit: 1}
	f.responses["image exists "+baseTag(t)] = response{Exit: 1}
	f.responses["build *"] = response{Output: "build rejected", Exit: 125}
	status, output, calls := f.run(t, "up", "demo")
	if status != 1 || !strings.Contains(output, "build rejected") || strings.Contains(output, "sandbox creation is not implemented") {
		t.Fatalf("status=%d output=%q", status, output)
	}
	for _, call := range calls {
		if len(call) > 0 && (call[0] == "create" || call[0] == "run" || call[0] == "volume") {
			t.Fatalf("sandbox work after failed build: %v", calls)
		}
	}
}

// TestWindowsImageBuildUsesCheckedTarget proves every image and sandbox lookup and build keeps the preflight-selected connection through RunWithHost and fake Podman.
func TestWindowsImageBuildUsesCheckedTarget(t *testing.T) {
	f := windowsHost(t)
	f.responses["--connection chosen container exists sandboxed-agents.default.demo"] = response{Exit: 1}
	f.responses["--connection chosen image exists "+baseTag(t)] = response{Exit: 1}
	f.responses["--connection chosen build *"] = response{}
	status, output, calls := f.run(t, "up", "demo")
	if status != 1 || !strings.Contains(output, "sandbox creation is not implemented (#14)") {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	buildFound := false
	for _, call := range calls {
		if len(call) > 0 && call[0] == "build" {
			t.Fatalf("unbound build: %v", call)
		}
		if len(call) > 2 && call[0] == "--connection" && call[1] == "chosen" && call[2] == "build" {
			buildFound = true
		}
	}
	if !buildFound {
		t.Fatalf("no bound build: %v", calls)
	}
}
