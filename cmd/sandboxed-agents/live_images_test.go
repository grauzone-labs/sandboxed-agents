package main_test

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/capture"
)

// Image coverage has a separate opt-in because build renews images for every
// controller group on the selected Podman host. The generic live opt-in is not
// enough: the suite's images case runs only with SANDBOXED_AGENTS_LIVE_IMAGES_TESTS=1.
const liveImagesEnv = "SANDBOXED_AGENTS_LIVE_IMAGES_TESTS"

// liveImageCoverage proves the base and four toolchains work as agent, run root sshd with persistent host keys and an Ed25519 key exchange, share base layers, carry image labels, and become outdated after the suite's one build at the real CLI, Podman, and SSH seams.
func liveImageCoverage(t *testing.T, suite *liveSuite) {
	host := suite.host
	assetHash := suite.assetHash
	sets := []string{"none", "dotnet", "playwright", "azure", "native"}
	images := make(map[string]liveImage)
	tags := make(map[string]string)

	if suite.imageWork == nil {
		for _, set := range sets {
			host.cleanupSandbox(t, "image-"+set)
			images[set], tags[set] = initialLiveImage(t, host, set)
		}
	} else {
		observed := make([]liveImage, len(sets))
		observedTags := make([]string, len(sets))
		for _, set := range sets {
			host.cleanupSandbox(t, "image-"+set)
		}
		// This is the last registered cleanup, so it joins other cases before any
		// image cleanup, including when an initial assertion terminates the case.
		t.Cleanup(func() { suite.joinIndependent() })
		t.Run("initial", func(t *testing.T) {
			for index, set := range sets {
				t.Run(set, func(t *testing.T) {
					t.Parallel()
					if !suite.imageWork.acquire() {
						t.Skip("not started after an earlier failure")
					}
					defer suite.imageWork.release()
					defer func() {
						if t.Failed() {
							suite.imageWork.suite.stateMu.Lock()
							suite.imageWork.suite.failed = true
							suite.imageWork.suite.stateMu.Unlock()
						}
					}()
					observed[index], observedTags[index] = initialLiveImage(t, host, set)
				})
			}
		})
		if t.Failed() {
			t.FailNow()
		}
		for index, set := range sets {
			if observedTags[index] == "" {
				t.Fatal("image coverage incomplete after an earlier failure")
			}
			images[set], tags[set] = observed[index], observedTags[index]
		}
	}

	for _, set := range sets {
		assertLiveImage(t, set, assetHash, images[set], images["none"])
	}
	if suite.imageWork == nil {
		host.assertNoBakedSSHKeys(t, tags["none"])
	} else {
		if !suite.imageWork.acquire() {
			t.Fatal("image coverage incomplete after an earlier failure")
		}
		func() { defer suite.imageWork.release(); host.assertNoBakedSSHKeys(t, tags["none"]) }()
	}
	if suite.beforeImageRebuild != nil {
		suite.beforeImageRebuild(t)
	}
	// This is the live suite's sole build command. up may build a missing
	// image, but must not renew an existing image. All five now exist.
	output := host.mustCLI(t, "", "build")
	suite.imagesRan = true
	for _, fragment := range []string{"keep", "image", "update", "list", "outdated"} {
		if !strings.Contains(strings.ToLower(output), fragment) {
			t.Fatalf("build omitted %q from its keep-images/update/list guidance:\n%q", fragment, output)
		}
	}
	rebuilt := make(map[string]liveImage)
	for _, set := range sets {
		rebuilt[set] = host.inspectImage(t, tags[set])
		if sameImageID(rebuilt[set].ID, images[set].ID) {
			t.Errorf("build did not replace the %s image (%q)", set, images[set].ID)
		}
		container := host.inspectContainer(t, "image-"+set)
		if !sameImageID(container.Image, images[set].ID) {
			t.Errorf("build replaced existing sandbox image for %s: got %q; want %q", set, container.Image, images[set].ID)
		}
	}
	for _, set := range sets {
		assertLiveImage(t, set, assetHash, rebuilt[set], rebuilt["none"])
	}
	// The rebuilt base outdates the base sandbox as much as the toolchain ones.
	listing := host.mustCLI(t, "", "list")
	for _, set := range sets {
		name := "image-" + set
		row, listed := liveListRow(listing, name)
		if !listed {
			t.Errorf("list omitted %s:\n%q", name, listing)
		} else if !strings.Contains(strings.ToLower(row), "outdated") {
			t.Errorf("list did not mark %s outdated: %q", name, row)
		}
	}
	for _, set := range sets {
		host.mustCLI(t, "", "remove", "image-"+set, "--volumes")
		host.assertNoSandboxObjects(t, "image-"+set)
	}
	host.assertNoGroupObjects(t)
	t.Logf("image coverage complete on %s: base and four toolchains, one build; controller group %s", runtime.GOOS, host.group)
}

// initialLiveImage retains every initial sandbox, SSH, smoke, swap and image
// identity assertion for one toolchain before the suite's exclusive rebuild.
func initialLiveImage(t *testing.T, host liveHost, set string) (liveImage, string) {
	name := "image-" + set
	host.mustCLI(t, "", "up", name, "--with", set)
	key := host.sshHostKey(t, name)
	host.assertSSHServer(t, name, key)
	if set == "none" {
		host.mustCLI(t, "", "stop", name)
		host.mustCLI(t, "", "start", name)
		if restarted := host.sshHostKey(t, name); restarted != key {
			t.Fatalf("restart changed the SSH host key: before=%q after=%q", key, restarted)
		}
		host.assertSSHServer(t, name, key)
	}
	host.mustCLI(t, liveAgentIdentity+liveImageSmoke(set), "shell", name)
	container := host.inspectContainer(t, name)
	host.assertNoSwap(t, name, container)
	if strings.Contains(container.Config.Image, host.group) {
		t.Fatalf("image tag includes controller group: %q", container.Config.Image)
	}
	tag := container.Config.Image
	image := host.inspectImage(t, container.Config.Image)
	if !sameImageID(image.ID, container.Image) {
		t.Fatalf("sandbox %s uses %q rather than tagged image %q", name, container.Image, image.ID)
	}
	return image, tag
}

// assertNoBakedSSHKeys inspects saved live image layers for embedded SSH host keys.
func (host liveHost) assertNoBakedSSHKeys(t *testing.T, tag string) {
	t.Helper()
	// Saving the image reads its actual layers. Looking inside the running
	// sandbox would instead read its /etc/ssh volume and could mistake freshly
	// generated host keys for image contents. Stream without storing the archive.
	ctx, cancel := context.WithTimeout(context.Background(), host.operationBound("image archive"))
	defer cancel()
	command := host.podmanCommand(context.Background(), "image", "save", "--format", "docker-archive", tag)
	// RunOwned owns cancellation; the command has no separate context watcher.
	command.Cancel = nil
	started := time.Now()
	host.reportOperation("image archive", "started")
	processErr := fmt.Errorf("image save did not finish")
	defer func() {
		host.reportOperation("image archive", fmt.Sprintf("finished after %s", time.Since(started).Round(time.Millisecond)))
		host.recordOperation("podman", "image archive", started, processErr)
		host.observeFailure("podman", "image archive", processErr)
	}()
	stderr := liveOutput{lines: liveLineStream{report: host.report}}
	stream, writer := io.Pipe()
	defer stream.Close()
	done := make(chan error, 1)
	go func() {
		err := capture.RunOwned(ctx, command, writer, &stderr)
		stderr.Flush()
		writer.Close()
		done <- err
	}()
	var err error
	archive := tar.NewReader(stream)
	layers := 0
	var keys []string
	for err == nil {
		var header *tar.Header
		header, err = archive.Next()
		if err != nil {
			break
		}
		// Docker archives may use either DIGEST/layer.tar or DIGEST.tar.
		if !header.FileInfo().Mode().IsRegular() || !strings.HasSuffix(header.Name, ".tar") {
			continue
		}
		layers++
		layer := tar.NewReader(archive)
		for {
			entry, layerErr := layer.Next()
			if layerErr == io.EOF {
				break
			}
			if layerErr != nil {
				err = layerErr
				break
			}
			name := strings.TrimPrefix(entry.Name, "./")
			if strings.HasPrefix(name, "etc/ssh/ssh_host_") && (strings.HasSuffix(name, "_key") || strings.HasSuffix(name, "_key.pub")) {
				keys = append(keys, name)
			}
		}
	}
	if err != io.EOF {
		cancel()
	}
	// Consume trailing archive padding before waiting: a writer may still have
	// bytes after the tar end marker and must not block on its stdout pipe.
	_, drainErr := io.Copy(io.Discard, stream)
	waitErr := <-done
	if ctx.Err() != nil {
		waitErr = fmt.Errorf("live Podman operation image archive ended after %s: %w; engine operations may still be running", time.Since(started).Round(time.Millisecond), errors.Join(ctx.Err(), waitErr))
	}
	processErr = waitErr
	if err != io.EOF || drainErr != nil || waitErr != nil || layers == 0 {
		t.Fatalf("read base image layer archive: tar=%v drain=%v process=%v layers=%d\n%q", err, drainErr, waitErr, layers, stderr.String())
	}
	if len(keys) != 0 {
		t.Errorf("base image layers contain baked SSH host keys: %q", keys)
	}
}

// assertLiveImage checks live image labels and verifies toolchain images extend the base image layers.
func assertLiveImage(t *testing.T, set, assetHash string, actual, base liveImage) {
	t.Helper()
	for key, value := range map[string]string{
		"io.sandboxed-agents.image":        "sandboxed-agents",
		"io.sandboxed-agents.asset-sha256": assetHash,
		"io.sandboxed-agents.toolchains":   set,
	} {
		if actual.Config.Labels[key] != value {
			t.Errorf("%s image label %s = %q; want %q", set, key, actual.Config.Labels[key], value)
		}
	}
	if set == "none" {
		return
	}
	if sameImageID(actual.ID, base.ID) {
		t.Errorf("%s image has the base image ID %q", set, actual.ID)
	}
	if !sameImageID(actual.Config.Labels["io.sandboxed-agents.base-image"], base.ID) {
		t.Errorf("%s image base label = %q; want %q", set, actual.Config.Labels["io.sandboxed-agents.base-image"], base.ID)
	}
	if len(base.RootFS.Layers) == 0 || len(actual.RootFS.Layers) <= len(base.RootFS.Layers) || !slices.Equal(actual.RootFS.Layers[:min(len(actual.RootFS.Layers), len(base.RootFS.Layers))], base.RootFS.Layers) {
		t.Errorf("%s image does not extend the base layers: base=%q toolchain=%q", set, base.RootFS.Layers, actual.RootFS.Layers)
	}
}

const liveAgentIdentity = `set -euo pipefail
test "$(id -u)" = 1000
test "$(id -g)" = 1000
test "$(id -un)" = agent
test "$HOME" = /home/agent
test "$PWD" = /workspace
test -s /usr/local/share/sandboxed-agents/versions.txt
`

// liveImageSmoke returns shell checks for the programs supplied by the selected toolchain image.
func liveImageSmoke(set string) string {
	switch set {
	case "none":
		return `. /etc/os-release
test "$ID" = debian
test "$VERSION_ID" = 13
test "$VERSION_CODENAME" = trixie
test "$(node -p 'process.versions.node.split(".")[0]')" = 24
npm --version
for command in ssh ssh-keygen git gh tmux curl jq rg less unzip; do command -v "$command"; done
test -x /usr/sbin/sshd
test -r /etc/ssl/certs/ca-certificates.crt
sandboxed-agents-manager version
for command in dotnet playwright az gcc g++ make cmake codex claude; do
  if command -v "$command"; then echo "unexpected base image program: $command" >&2; exit 1; fi
done
`
	case "dotnet":
		return "dotnet --list-sdks | tee /tmp/live-dotnet-sdks\ntest -s /tmp/live-dotnet-sdks\n"
	case "playwright":
		return `node <<'JS'
const { chromium, firefox, webkit } = require('playwright');
(async () => {
  for (const [name, type] of Object.entries({chromium, firefox, webkit})) {
    const browser = await type.launch({headless: true});
    try {
      const page = await browser.newPage();
      await page.setContent('<title>live smoke</title>');
      if (await page.title() !== 'live smoke') throw new Error(name + ' page check failed');
      console.log(name + ': ' + browser.version());
    } finally { await browser.close(); }
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
JS
`
	case "azure":
		return `directory=$(mktemp -d)
trap 'rm -rf "$directory"' EXIT
az version >"$directory/version.json" 2>"$directory/version.stderr"
cat "$directory/version.json" "$directory/version.stderr"
if grep -Fq 'Error loading command module' "$directory/version.stderr" "$directory/version.json"; then exit 1; fi
jq -e '."azure-cli" | type == "string" and length > 0' "$directory/version.json"
az monitor --help
az postgres --help
az mysql --help
az extension list >"$directory/extensions.json"
cat "$directory/extensions.json"
jq -e 'any(.[]; .name == "azure-devops" and (.version | type == "string" and length > 0))' "$directory/extensions.json"
# Login strips ambient AZURE_* variables; discovery must use the CLI's default system directory.
env -i HOME="$HOME" PATH="$PATH" AZURE_CONFIG_DIR="$HOME/.azure" AZURE_CORE_COLLECT_TELEMETRY=0 az devops --help >"$directory/devops.help"
cat "$directory/devops.help"
for group in project team user; do
  grep -Eq "^[[:space:]]+$group[[:space:]]+:" "$directory/devops.help"
done
env -i PATH="$PATH" check-azure-credentials
`
	case "native":
		return `gcc --version
g++ --version
make --version
cmake --version
directory=$(mktemp -d /workspace/live-native.XXXXXX)
trap 'rm -rf "$directory"' EXIT
printf '%s\n' '#include <stdio.h>' 'int main(void) { puts("native smoke"); return 0; }' > "$directory/main.c"
gcc -Wall -Wextra -Werror "$directory/main.c" -o "$directory/smoke"
test "$("$directory/smoke")" = 'native smoke'
`
	default:
		panic(fmt.Sprintf("unknown live smoke toolchain %q", set))
	}
}
