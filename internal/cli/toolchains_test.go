package cli_test

import (
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
)

// TestUnknownToolchainsPrecedeHostAndOwnerChecks proves unknown names list every delivered value before any Podman call at the public CLI seam.
func TestUnknownToolchainsPrecedeHostAndOwnerChecks(t *testing.T) {
	for _, command := range []string{"build", "up", "update"} {
		t.Run(command, func(t *testing.T) {
			f := linuxHost(t)
			f.responses["--version"] = response{Exit: 125, Output: "broken preflight"}
			f.responses["container inspect sandboxed-agents.default.demo"] = response{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"other"}}}]`}
			args := []string{command}
			if command != "build" {
				args = append(args, "demo")
			}
			args = append(args, "--with", "bogus")
			status, output, calls := f.run(t, args...)
			if status != 2 || len(calls) != 0 {
				t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
			}
			for _, name := range []string{"unknown toolchain", "dotnet", "playwright", "azure", "native", "none"} {
				if !strings.Contains(output, name) {
					t.Errorf("diagnostic lacks %s: %s", name, output)
				}
			}
		})
	}
}

// TestToolchainSelectionsAreSets proves the parser accepts delivered toolchains and canonicalizes ordering and duplicates at the public parser seam.
func TestToolchainSelectionsAreSets(t *testing.T) {
	request, err := cli.Parse([]string{"up", "demo", "--with", "playwright,dotnet,dotnet"}, "default")
	if err != nil || strings.Join(request.Toolchains, ",") != "dotnet,playwright" {
		t.Fatalf("request=%+v error=%v", request, err)
	}
}

// TestUpChecksRecordedToolchainSet proves matching sets can reuse a sandbox and differing sets recommend update without starting through the public CLI seam.
func TestUpChecksRecordedToolchainSet(t *testing.T) {
	for _, value := range []string{"playwright,dotnet", "native"} {
		t.Run(value, func(t *testing.T) {
			f := linuxHost(t)
			f.responses["container exists sandboxed-agents.default.demo"] = response{}
			f.responses["container inspect sandboxed-agents.default.demo"] = response{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.toolchains":"dotnet,playwright"}},"State":{"Running":true}}]`}
			f.responses["volume exists *"] = response{Exit: 1}
			f.responses["container exists sandboxed-agents-backup.default.demo"] = response{Exit: 1}
			status, output, calls := f.run(t, "up", "demo", "--with", value)
			want := 0
			if value == "native" {
				want = 1
			}
			if status != want || (want == 1 && !strings.Contains(output, "update demo --with native")) {
				t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
			}
			noMutation(t, calls)
		})
	}
}

// TestBuildRefreshesBaseAndExistingToolchainSets proves build discovers sets by labels, refreshes base first without cache, and adds a requested absent set through the CLI and fake Podman seam.
func TestBuildRefreshesBaseAndExistingToolchainSets(t *testing.T) {
	f := linuxHost(t)
	f.responses["image ls *"] = response{Output: `[{"Id":"old-native"}]`}
	f.responses["image inspect old-native"] = response{Output: `[{"Id":"old-native","Labels":{"io.sandboxed-agents.toolchains":"native"}}]`}
	f.responses["build *"] = response{}
	f.responses["image inspect "+baseTag(t)] = response{Output: `[{"Id":"sha256:base-new"}]`}
	status, output, calls := f.run(t, "build", "--with", "dotnet")
	if status != 0 || !strings.Contains(output, "existing sandboxes keep their image until update") || !strings.Contains(output, "list") {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	var builds [][]string
	for _, call := range calls {
		if call[0] == "build" {
			builds = append(builds, call)
		}
	}
	if len(builds) != 3 {
		t.Fatalf("build calls=%v", builds)
	}
	for i, call := range builds {
		joined := strings.Join(call, " ")
		if !strings.Contains(joined, "--no-cache") || !strings.Contains(joined, "--label io.sandboxed-agents.image=sandboxed-agents") || !strings.Contains(joined, "--label io.sandboxed-agents.asset-sha256=") {
			t.Fatalf("missing rebuild flags or labels: %v", call)
		}
		if i == 0 && (!strings.Contains(joined, "--pull=always") || !strings.Contains(joined, "--tag "+baseTag(t))) {
			t.Fatalf("base not rebuilt first: %v", call)
		}
		if i > 0 && (!strings.Contains(joined, "--label io.sandboxed-agents.base-image=sha256:base-new") || !strings.Contains(joined, "--build-arg BASE_IMAGE=sha256:base-new")) {
			t.Fatalf("toolchain does not use refreshed base: %v", call)
		}
	}
	for _, set := range []string{"dotnet", "native"} {
		if !strings.Contains(strings.Join(builds[1], " ")+strings.Join(builds[2], " "), "--label io.sandboxed-agents.toolchains="+set) {
			t.Fatalf("set missing %s: %v", set, builds)
		}
	}
}

// TestFailedBaseBuildPreventsToolchainBuilds proves an ordinary or fingerprint base failure never reaches a toolchain build or removes old images through the CLI seam.
func TestFailedBaseBuildPreventsToolchainBuilds(t *testing.T) {
	for _, cause := range []string{"base build failed", "expected fingerprint PIN found OTHER"} {
		t.Run(cause, func(t *testing.T) {
			f := linuxHost(t)
			f.responses["image ls *"] = response{Output: `[]`}
			f.responses["build *"] = response{Exit: 125, Output: cause}
			status, output, calls := f.run(t, "build", "--with", "native")
			if status != 1 || !strings.Contains(output, cause) {
				t.Fatalf("status=%d output=%q", status, output)
			}
			builds := 0
			for _, call := range calls {
				if call[0] == "build" {
					builds++
					if !strings.Contains(strings.Join(call, " "), "--tag "+baseTag(t)) {
						t.Fatalf("toolchain built after base failure: %v", calls)
					}
				}
				if call[0] == "rmi" || (call[0] == "image" && len(call) > 1 && call[1] == "rm") {
					t.Fatalf("previous image removed: %v", calls)
				}
			}
			if builds != 1 {
				t.Fatalf("build calls=%v", calls)
			}
		})
	}
}

// TestBuildContinuesAfterToolchainFailure proves one failed set retains its tag, remaining sets build, and the final failure names the set and update note through the CLI and fake Podman seam.
func TestBuildContinuesAfterToolchainFailure(t *testing.T) {
	f := linuxHost(t)
	f.responses["image ls *"] = response{Output: `[{"Id":"native-old"},{"Id":"azure-old"}]`}
	for _, name := range []string{"native", "azure"} {
		f.responses["image inspect "+name+"-old"] = response{Output: `[{"Id":"` + name + `-old","Labels":{"io.sandboxed-agents.toolchains":"` + name + `"}}]`}
	}
	f.responses["image inspect "+baseTag(t)] = response{Output: `[{"Id":"base-new"}]`}
	f.responses["build *"] = response{}
	f.responses["build * --label io.sandboxed-agents.toolchains=azure *"] = response{Exit: 125, Output: "azure rejected"}
	status, output, calls := f.run(t, "build")
	if status != 1 || !strings.Contains(output, "failed toolchain sets: azure") || !strings.Contains(output, "built image localhost/sandboxed-agents:native-") || !strings.Contains(output, "existing sandboxes keep their image until update") {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	builds := 0
	for _, call := range calls {
		if call[0] == "build" {
			builds++
		}
		if call[0] == "rmi" || (call[0] == "image" && call[1] == "rm") {
			t.Fatalf("old image removed: %v", calls)
		}
	}
	if builds != 3 {
		t.Fatalf("remaining build skipped: %v", calls)
	}
}

// TestUpRebuildsToolchainImagesOnTheCurrentBase proves cached images tied to an old base are replaced before creation and current images skip building through the CLI seam.
func TestUpRebuildsToolchainImagesOnTheCurrentBase(t *testing.T) {
	for _, base := range []string{"base-old", "base-new"} {
		t.Run(base, func(t *testing.T) {
			f := linuxHost(t)
			scriptNewSandbox(t, f, "default", "demo")
			f.responses["image inspect "+baseTag(t)] = response{Output: `[{"Id":"base-new"}]`}
			f.responses["image exists localhost/sandboxed-agents:native-*"] = response{}
			f.responses["image inspect localhost/sandboxed-agents:native-*"] = response{Output: `[{"Id":"native-old","Labels":{"io.sandboxed-agents.base-image":"` + base + `"}}]`}
			f.responses["build *"] = response{}
			status, output, calls := f.run(t, "up", "demo", "--with", "native")
			if status != 0 {
				t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
			}
			builds := 0
			for _, call := range calls {
				if call[0] == "build" {
					builds++
					joined := strings.Join(call, " ")
					if !strings.Contains(joined, "BASE_IMAGE=base-new") || strings.Contains(joined, "--no-cache") {
						t.Fatalf("wrong missing-image build: %v", call)
					}
				}
			}
			want := 0
			if base == "base-old" {
				want = 1
			}
			if builds != want {
				t.Fatalf("builds=%d want=%d calls=%v", builds, want, calls)
			}
		})
	}
}

// TestListMarksImagesOutdatedAfterBaseRefresh proves a sandbox is outdated when its toolchain image uses the previous base even if its own image ID matches the tag through the public CLI seam.
func TestListMarksImagesOutdatedAfterBaseRefresh(t *testing.T) {
	f := linuxHost(t)
	f.responses["ps --all --format json"] = response{Output: `[{"Names":["sandboxed-agents.default.demo"]}]`}
	f.responses["volume ls --format json"] = response{Output: `[]`}
	f.responses["container exists sandboxed-agents.default.demo"] = response{}
	f.responses["container inspect sandboxed-agents.default.demo"] = response{Output: `[{"Image":"native-current","Config":{"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.toolchains":"native"}},"State":{"Running":false}}]`}
	f.responses["volume exists *"] = response{Exit: 1}
	f.responses["container exists sandboxed-agents-backup.default.demo"] = response{Exit: 1}
	f.responses["image exists *"] = response{}
	f.responses["image inspect localhost/sandboxed-agents:native-*"] = response{Output: `[{"Id":"native-current","Labels":{"io.sandboxed-agents.base-image":"base-old"}}]`}
	f.responses["image inspect "+baseTag(t)] = response{Output: `[{"Id":"base-new"}]`}
	status, output, calls := f.run(t, "list")
	if status != 0 || !strings.Contains(output, "outdated") || !strings.Contains(output, "native") {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	noMutation(t, calls)
}

// TestToolchainImagesAreSharedAcrossControllerGroups proves equal canonical sets choose the same group-independent image and recorded label through the CLI and fake Podman seam.
func TestToolchainImagesAreSharedAcrossControllerGroups(t *testing.T) {
	var tags []string
	for _, group := range []string{"first", "second"} {
		f := linuxHost(t)
		t.Setenv("SANDBOXED_AGENTS_GROUP", group)
		scriptNewSandbox(t, f, group, "demo")
		f.responses["image inspect "+baseTag(t)] = response{Output: `[{"Id":"base-current"}]`}
		f.responses["image exists localhost/sandboxed-agents:dotnet-playwright-*"] = response{}
		f.responses["image inspect localhost/sandboxed-agents:dotnet-playwright-*"] = response{Output: `[{"Id":"tools-current","Labels":{"io.sandboxed-agents.base-image":"base-current"}}]`}
		status, output, calls := f.run(t, "up", "demo", "--with", "playwright,dotnet,dotnet")
		if status != 0 {
			t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
		}
		for _, call := range calls {
			if call[0] == "build" {
				t.Fatalf("rebuilt shared image: %v", calls)
			}
			if call[0] == "create" {
				tags = append(tags, call[len(call)-1])
				if !strings.Contains(strings.Join(call, " "), "--label io.sandboxed-agents.toolchains=dotnet,playwright") {
					t.Fatalf("missing recorded set: %v", call)
				}
			}
		}
	}
	if len(tags) != 2 || tags[0] != tags[1] || !strings.HasPrefix(tags[0], "localhost/sandboxed-agents:dotnet-playwright-") {
		t.Fatalf("images differ between groups: %v", tags)
	}
}

// TestWindowsRebuildUsesOnlyTheCheckedTarget proves image discovery, builds, and base inspection retain the preflight-selected connection through the CLI seam.
func TestWindowsRebuildUsesOnlyTheCheckedTarget(t *testing.T) {
	f := windowsHost(t)
	f.responses["--connection chosen image ls *"] = response{Output: `[]`}
	f.responses["--connection chosen build *"] = response{}
	f.responses["--connection chosen image inspect "+baseTag(t)] = response{Output: `[{"Id":"base-new"}]`}
	status, output, calls := f.run(t, "build", "--with", "native")
	if status != 0 {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	for _, call := range calls {
		if call[0] == "image" || call[0] == "build" {
			t.Fatalf("unbound image operation: %v", call)
		}
	}
	builds := 0
	for _, call := range calls {
		if len(call) > 2 && call[2] == "build" {
			builds++
			if call[0] != "--connection" || call[1] != "chosen" {
				t.Fatalf("wrong target: %v", call)
			}
		}
	}
	if builds != 2 {
		t.Fatalf("missing builds: %v", calls)
	}
}
