package main_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// TestLivePodmanVersion checks that Podman is available and its version command succeeds at the live process boundary, only when explicitly opted in.
func TestLivePodmanVersion(t *testing.T) {
	if os.Getenv(testutil.LiveTestsEnv) != "1" {
		t.Skip("set " + testutil.LiveTestsEnv + "=1 to opt in")
	}
	t.Log("live test body selected")
	if _, err := exec.LookPath("podman"); err != nil {
		t.Fatalf("Podman is unavailable: %v", err)
	}
	if output, err := exec.Command("podman", "version").CombinedOutput(); err != nil {
		t.Fatalf("Podman is unusable: %v: %s", err, output)
	}
}

// TestLiveImagesRequiresSeparateOptIn proves generic live selection and nonliteral image opt-ins skip image coverage, while its literal opt-in reaches the CLI preflight, through the subprocess test and fake Podman seams.
func TestLiveImagesRequiresSeparateOptIn(t *testing.T) {
	for _, value := range []string{"", "0", "true", "1"} {
		t.Run("image-opt-in-"+value, func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			testExecutable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(testExecutable, "-test.v", "-test.run=^TestLiveImages$")
			command.Env = testutil.Env(fakes.Env(os.Environ()), map[string]string{
				testutil.LiveTestsEnv: "1", liveImagesEnv: value,
				"TEST_SANDBOXED_AGENTS_EXECUTABLE": executable,
				"TEST_FAKE_EXECUTABLE":             fakeExecutable, "TEST_RELEASE_EXECUTABLE": releaseExecutable,
			})
			output, err := command.CombinedOutput()
			selected := strings.Contains(string(output), "live test body selected (images")
			calls := testutil.Invocations(t, fakes.PodmanLog)
			if value == "1" {
				// Empty fake prerequisite answers deliberately fail the preflight,
				// before any image preparation, build, or sandbox creation.
				if !selected || err == nil || len(calls) == 0 || !strings.Contains(string(output), "step 2:") {
					t.Fatalf("image opt-in did not reach a failing fake preflight: selected=%v calls=%v err=%v\n%s", selected, calls, err, output)
				}
				for _, call := range calls {
					for _, argument := range call {
						if argument == "build" || argument == "create" {
							t.Fatalf("failed image preflight started a mutation: %v", calls)
						}
					}
				}
			} else if selected || err != nil || len(calls) != 0 || !strings.Contains(string(output), "--- SKIP: TestLiveImages") {
				t.Fatalf("generic live opt-in or %q selected image coverage: selected=%v calls=%v err=%v\n%s", value, selected, calls, err, output)
			}
		})
	}
}
