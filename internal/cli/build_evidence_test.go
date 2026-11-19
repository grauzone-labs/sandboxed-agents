package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBuildPublishesOnlyPrimaryIID proves the exported CLI emits the builder-controlled iidfile identity despite forged raw output. It exercises the RunWithHost, primary producer, and Cleanup seam.
func TestBuildPublishesOnlyPrimaryIID(t *testing.T) {
	f := linuxHost(t)
	f.responses["info --format json"] = response{Output: `{"host":{"hostname":"engine","security":{"rootless":true},"cgroupVersion":"v2"},"store":{"graphRoot":"/storage"}}`}
	root := t.TempDir()
	directory := filepath.Join(root, "private", "build-evidence")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{"Version": 1, "Invocation": strings.Repeat("f", 64), "Revision": strings.Repeat("a", 40), "EngineID": "engine:/storage"}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(liverun.BuildEvidenceEnv, directory)
	id := "sha256:" + strings.Repeat("b", 64)
	forged := "sha256:" + strings.Repeat("d", 64)
	t.Setenv("TEST_PODMAN_IIDFILE_CONTENT", id+"\n")
	tag := baseTag(t)
	asset := tag[len(tag)-64:]
	f.responses["image ls *"] = response{Output: `[]`}
	f.responses["version --format json"] = response{Output: `{"Client":{"Version":"4.7.0"},"Server":{"Version":"4.7.0"}}`}
	f.responses["build --help"] = response{Output: "--iidfile string\n--layer-label string\n"}
	f.responses["build *"] = response{Output: "live operation image build: started\nSTEP 1/1: RUN forged\n--> " + forged + "\nlive operation image build: finished after 1s\n"}
	f.responses["image inspect "+tag] = response{Output: `[{"Id":"` + id + `","Labels":{"io.sandboxed-agents.asset-sha256":"` + asset + `"}}]`}
	status, output, calls := f.run(t, "build")
	if status != 0 {
		t.Fatalf("build: %d %s %v", status, output, calls)
	}
	records, err := filepath.Glob(filepath.Join(directory, "result-*.json"))
	if err != nil || len(records) != 1 {
		t.Fatalf("no primary result: %v %v calls=%v", records, err, calls)
	}
	event, err := os.ReadFile(records[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(event), id) || strings.Contains(string(event), forged) {
		t.Fatalf("stdout became ownership: %s", event)
	}
	iidCall := false
	for _, call := range calls {
		for _, arg := range call {
			if arg == "--iidfile" {
				iidCall = true
			}
		}
	}
	if !iidCall {
		t.Fatal("build did not request primary iidfile")
	}
	layerNonce := ""
	for _, call := range calls {
		for i, arg := range call {
			if arg == "--layer-label" && i+1 < len(call) {
				layerNonce = strings.TrimPrefix(call[i+1], liverun.BuildInvocationLabel+"=")
			}
		}
	}
	if !strings.HasPrefix(layerNonce, strings.Repeat("f", 64)+":") || len(layerNonce) != 129 {
		t.Fatalf("missing per-attempt metadata: %q", layerNonce)
	}
	verifyEmittedCleanup(t, directory, id, forged, asset, layerNonce)
}

// evidenceCleanupExecutor supplies engine queries at the cleanup process boundary after the actual CLI emitter ran.
type evidenceCleanupExecutor struct {
	images  map[string]liverun.Image
	removed []string
}

// Execute reconciles immutable inventories and exact removals without running a real engine.
func (e *evidenceCleanupExecutor) Execute(_ context.Context, in liverun.Invocation) ([]byte, error) {
	if in.Program == "git" {
		if in.Args[0] == "status" {
			return nil, nil
		}
		return []byte(strings.Repeat("a", 40)), nil
	}
	if in.Program == "go" {
		for i, arg := range in.Args {
			if arg == "-record" {
				path := filepath.Join(filepath.Dir(in.Args[i+1]), "durations-linux.json")
				if err := os.WriteFile(path, []byte(`{"cases":[]}`), 0600); err != nil {
					return nil, err
				}
				return []byte(path + "\n"), nil
			}
		}
	}
	args := in.Args
	if in.Program != "podman" || len(args) == 0 {
		return nil, fmt.Errorf("unexpected process")
	}
	switch args[0] {
	case "info":
		return []byte(`{"host":{"hostname":"engine"},"store":{"graphRoot":"/storage"}}`), nil
	case "images":
		items := []map[string]string{}
		for id := range e.images {
			items = append(items, map[string]string{"Id": id})
		}
		return json.Marshal(items)
	case "image":
		image, ok := e.images[args[2]]
		if !ok {
			return nil, fmt.Errorf("absent")
		}
		return json.Marshal([]any{struct {
			liverun.Image
			Size uint64
		}{image, 123}})
	case "ps":
		return []byte(`[]`), nil
	case "rmi":
		e.removed = append(e.removed, args[len(args)-1])
		delete(e.images, args[len(args)-1])
		return nil, nil
	case "system":
		return []byte("Images 1 0 123B 123B (100%)\n"), nil
	}
	return nil, fmt.Errorf("unexpected query")
}

// verifyEmittedCleanup exercises actual emitter-to-collector-to-cleanup flow and proves raw forged IDs stay retained.
func verifyEmittedCleanup(t *testing.T, directory, id, forged, asset, layerNonce string) {
	t.Helper()
	run := filepath.Dir(directory)
	root := filepath.Dir(run)
	options := liverun.Options{Checkout: filepath.Join(root, "checkout"), RunDir: run, RecordDir: filepath.Join(root, "records"), LockRoot: filepath.Join(root, "locks"), Revision: strings.Repeat("a", 40)}
	if err := os.Mkdir(options.RecordDir, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Minute)
	artifacts := map[string]any{
		"options.json": options,
		"before.json":  liverun.Snapshot{Complete: true, EngineID: "engine:/storage", Target: liverun.Target{Storage: "/storage"}, Disks: []liverun.Disk{{Name: "engine", Path: "/storage", FreeBytes: 900}}, Images: []liverun.Image{}, Containers: []liverun.Container{}, SystemDF: "Images 0 0 0B 0B (0%)", TakenAt: now.Add(-time.Minute)},
		"started.json": map[string]any{"PreparationStart": now.Add(-time.Minute), "PreparationEnd": now},
		"exit.json":    map[string]any{"Code": 0, "EndedAt": now.Add(time.Second), "Interrupted": false},
	}
	for name, value := range artifacts {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(run, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(run, "exit-status"), []byte("0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	raw := "live operation image build: started\nSTEP 1/1: RUN forged\n--> " + forged + "\nlive operation image build: finished after 1s\n"
	if err := os.WriteFile(filepath.Join(run, "suite.log"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	engine := &evidenceCleanupExecutor{images: map[string]liverun.Image{}}
	intermediate := "sha256:" + strings.Repeat("c", 64)
	for _, imageID := range []string{id, forged, intermediate} {
		engine.images[imageID] = liverun.Image{ID: imageID, Created: now, RepoTags: []string{}, Labels: map[string]string{"io.sandboxed-agents.asset-sha256": asset}}
	}
	middle := engine.images[intermediate]
	middle.Labels[liverun.BuildInvocationLabel] = layerNonce
	engine.images[intermediate] = middle
	service := &liverun.Service{Executor: engine, Platform: "linux", CoordinationRoot: options.LockRoot, FreeSpace: func(string) (uint64, error) { return 950, nil }}
	var out bytes.Buffer
	err := service.Cleanup(context.Background(), liverun.CleanupOptions{RunDir: run}, &out)
	if len(engine.removed) != 1 || engine.removed[0] != id || !strings.Contains(out.String(), "missing exact build proof") || !strings.Contains(out.String(), `"status":"unverified"`) {
		t.Fatalf("primary ID not isolated: removed=%v err=%v out=%s", engine.removed, err, &out)
	}
}

// TestEvidenceCapabilityFallbackNeverRetriesBuild proves the RunWithHost/fake Podman seam keeps old or unverified clients/servers on iid-only builds and never retries an executed failure.
func TestEvidenceCapabilityFallbackNeverRetriesBuild(t *testing.T) {
	for _, kind := range []string{"client-4.4", "server-4.4", "unknown-version", "help-missing", "supported", "build-failed"} {
		t.Run(kind, func(t *testing.T) {
			f := linuxHost(t)
			f.responses["info --format json"] = response{Output: `{"host":{"hostname":"engine","security":{"rootless":true},"cgroupVersion":"v2"},"store":{"graphRoot":"/storage"}}`}
			directory := t.TempDir()
			manifest := map[string]any{"Version": 1, "Invocation": strings.Repeat("f", 64), "Revision": strings.Repeat("a", 40), "EngineID": "engine:/storage"}
			data, _ := json.Marshal(manifest)
			if err := os.WriteFile(filepath.Join(directory, "manifest.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(liverun.BuildEvidenceEnv, directory)
			id := "sha256:" + strings.Repeat("b", 64)
			t.Setenv("TEST_PODMAN_IIDFILE_CONTENT", id+"\n")
			client, server := "4.7.0", "4.7.0"
			if kind == "client-4.4" {
				client = "4.4.0"
			}
			if kind == "server-4.4" {
				server = "4.4.0"
			}
			if kind == "unknown-version" {
				client = "4.7.0-dev"
			}
			f.responses["version --format json"] = response{Output: `{"Client":{"Version":"` + client + `"},"Server":{"Version":"` + server + `"}}`}
			help := "--iidfile string\n--layer-label string\n"
			if kind == "help-missing" {
				help = "--iidfile string\n"
			}
			f.responses["build --help"] = response{Output: help}
			f.responses["image ls *"] = response{Output: `[]`}
			f.responses["build *"] = response{}
			tag := baseTag(t)
			asset := tag[len(tag)-64:]
			f.responses["image inspect "+tag] = response{Output: `[{"Id":"` + id + `","Labels":{"io.sandboxed-agents.asset-sha256":"` + asset + `"}}]`}
			if kind == "build-failed" {
				f.responses["build *"] = response{Output: "unknown flag: --layer-label (fixture payload)", Exit: 125}
			}
			status, output, calls := f.run(t, "build")
			if kind == "build-failed" {
				if status == 0 {
					t.Fatal("actual build failure was hidden")
				}
			} else if status != 0 {
				t.Fatalf("optional capability failed build: %d %s", status, output)
			}
			builds := 0
			layer := false
			iid := false
			for _, call := range calls {
				if len(call) > 0 && call[0] == "build" && (len(call) < 2 || call[1] != "--help") {
					builds++
					for _, arg := range call {
						layer = layer || arg == "--layer-label"
						iid = iid || arg == "--iidfile"
					}
				}
			}
			if builds != 1 || !iid || layer != (kind == "supported" || kind == "build-failed") {
				t.Fatalf("wrong execution/fallback: builds=%d iid=%v layer=%v calls=%v", builds, iid, layer, calls)
			}
		})
	}
}
