package liverun_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrepareConcurrencyInputs proves explicit/default concurrency is saved and
// invalid public values are refused before engine work at the prepare command seam.
func TestPrepareConcurrencyInputs(t *testing.T) {
	for _, test := range []struct {
		name          string
		flags         []string
		overlap, jobs int
		invalid       bool
	}{
		{"default", nil, 1, 1, false}, {"explicit", []string{"-overlap", "3", "-build-jobs", "2"}, 3, 2, false},
		{"zero-jobs", []string{"-build-jobs", "0"}, 0, 0, true}, {"negative-overlap", []string{"-overlap", "-1"}, 0, 0, true}, {"invalid", []string{"-overlap", "no"}, 0, 0, true},
		{"zero-overlap", []string{"-overlap", "0"}, 0, 0, true}, {"negative-jobs", []string{"-build-jobs", "-1"}, 0, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, fake, args, directory := commandHarness(t)
			args = append(args, test.flags...)
			var output bytes.Buffer
			err := svc.RunDispatch(context.Background(), args, &output, &output)
			if test.invalid {
				if err == nil || len(fake.calls) != 0 {
					t.Fatalf("invalid inputs reached host: err=%v calls=%v", err, fake.calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var options struct{ Overlap, BuildJobs int }
			if err = json.Unmarshal(mustReadFile(t, filepath.Join(directory, "options.json")), &options); err != nil {
				t.Fatal(err)
			}
			if options.Overlap != test.overlap || options.BuildJobs != test.jobs {
				t.Fatalf("saved options missing configured inputs: %v", options)
			}
		})
	}
}

// TestRunUsesSavedConcurrencyInputs proves the supervisor explicitly overrides
// ambient concurrency from saved options, including absent legacy defaults, at
// the prepare/run fake-Go environment and status command seams.
func TestRunUsesSavedConcurrencyInputs(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit", true: "legacy"}[legacy], func(t *testing.T) {
			t.Setenv("SANDBOXED_AGENTS_LIVE_OVERLAP", "9")
			t.Setenv("SANDBOXED_AGENTS_LIVE_BUILD_JOBS", "9")
			root, private, _ := fixtureSetup(t, "-overlap", "3", "-build-jobs", "2")
			if legacy {
				var options map[string]any
				if err := json.Unmarshal(mustReadFile(t, filepath.Join(private, "options.json")), &options); err != nil {
					t.Fatal(err)
				}
				delete(options, "Overlap")
				delete(options, "BuildJobs")
				cleanupArtifact(t, private, "options.json", options)
			}
			command := exec.Command(os.Args[0], "run", "-run-dir", private)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("run: %v\n%s", err, output)
			}
			waitFixture(t, filepath.Join(root, "suite-ready"))
			var got map[string]string
			if err := json.Unmarshal(mustReadFile(t, filepath.Join(root, "suite-inputs.json")), &got); err != nil {
				t.Fatal(err)
			}
			overlap, jobs := "3", "2"
			if legacy {
				overlap, jobs = "1", "1"
			}
			if got["overlap"] != overlap || got["build_jobs"] != jobs {
				t.Fatalf("suite received %v; want %s/%s", got, overlap, jobs)
			}
			status := exec.Command(os.Args[0], "status", "-run-dir", private)
			data, err := status.CombinedOutput()
			if err != nil {
				t.Fatalf("status: %v\n%s", err, data)
			}
			if !strings.Contains(string(data), `"case_overlap":`+overlap) || !strings.Contains(string(data), `"build_jobs":`+jobs) {
				t.Fatalf("status omitted configured inputs: %s", data)
			}
		})
	}
}

// TestRunRefusesInvalidSavedConcurrency proves malformed private run settings
// stop before supervisor creation or engine work at the run command boundary.
func TestRunRefusesInvalidSavedConcurrency(t *testing.T) {
	svc, fake, args, directory := commandHarness(t)
	var output bytes.Buffer
	if err := svc.RunDispatch(context.Background(), args, &output, &output); err != nil {
		t.Fatal(err)
	}
	var options map[string]any
	if err := json.Unmarshal(mustReadFile(t, filepath.Join(directory, "options.json")), &options); err != nil {
		t.Fatal(err)
	}
	options["BuildJobs"] = -1
	cleanupArtifact(t, directory, "options.json", options)
	fake.calls = nil
	started := false
	svc.StartSupervisor = func(*exec.Cmd) error { started = true; return nil }
	if err := svc.RunDispatch(context.Background(), []string{"run", "-run-dir", directory}, &output, &output); err == nil || started || len(fake.calls) != 0 {
		t.Fatalf("invalid private settings reached host: err=%v started=%v calls=%v", err, started, fake.calls)
	}
}
