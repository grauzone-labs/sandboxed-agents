package image_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/grauzone-labs/sandboxed-agents/image"
	"github.com/grauzone-labs/sandboxed-agents/internal/releasetest"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// TestImageBuildFromCRLFCheckoutExecutables proves executables built with LF and CRLF embedded assets print identical hashes and tags and clean up actual process builds through the public image API against fake Podman.
func TestImageBuildFromCRLFCheckoutExecutables(t *testing.T) {
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	fake := filepath.Join(t.TempDir(), "fake"+suffix)
	command := exec.Command("go", "build", "-o", fake, "../internal/testutil/testdata/fake.go")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fake: %v\n%s", err, output)
	}
	var outputs []string
	for _, lineEndings := range []string{"LF", "CRLF"} {
		checkout := filepath.Join(t.TempDir(), lineEndings+" checkout")
		releasetest.CopyRepositorySources(t, "..", checkout)
		assetDir := filepath.Join(checkout, "image", "context")
		if lineEndings == "CRLF" {
			releasetest.CopyCRLFContext(t, image.Context(), assetDir)
			raw, err := os.ReadFile(filepath.Join(assetDir, "base", "nodesource-signing-key.fingerprint"))
			if err != nil || !strings.Contains(string(raw), "\r\n") {
				t.Fatalf("CRLF fingerprint fixture = %q, error = %v", raw, err)
			}
		} else if err := image.WriteContext(image.Context(), assetDir); err != nil {
			t.Fatal(err)
		}
		driver := filepath.Join(t.TempDir(), "build-driver"+suffix)
		command = exec.Command("go", "build", "-o", driver, "./image/testdata/build")
		command.Dir = checkout
		command.Env = testutil.Env(os.Environ(), map[string]string{"GOWORK": "off", "GOENV": "off", "GOTOOLCHAIN": "local", "GOFLAGS": ""})
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build %s executable: %v\n%s", lineEndings, err, output)
		}
		for _, status := range []string{"0", "23"} {
			fakes := testutil.Install(t, fake)
			command = exec.Command(driver)
			command.Env = testutil.Env(fakes.Env(os.Environ()), map[string]string{"TEST_PODMAN_EXIT": status})
			output, err := command.CombinedOutput()
			if status == "0" {
				if err != nil {
					t.Fatalf("%s build: %v\n%s", lineEndings, err, output)
				}
				outputs = append(outputs, string(output))
			} else if err == nil || !strings.Contains(string(output), "exit status 23") || strings.Contains(string(output), "image-tag ") {
				t.Fatalf("%s failed build: %v\n%s", lineEndings, err, output)
			}
			calls := testutil.Invocations(t, fakes.PodmanLog)
			if len(calls) != 1 || len(calls[0]) != 6 || calls[0][0] != "build" {
				t.Fatalf("Podman build calls = %q", calls)
			}
			if status == "0" && !strings.Contains(string(output), "image-tag "+calls[0][4]+"\n") {
				t.Fatalf("printed image tag does not match Podman argv: %q, %q", output, calls)
			}
			if _, err := os.Stat(filepath.Dir(calls[0][5])); !os.IsNotExist(err) {
				t.Fatalf("%s build left temporary context: %v", lineEndings, err)
			}
			if calls := testutil.Invocations(t, fakes.SSHLog); len(calls) != 0 {
				t.Fatalf("unexpected SSH calls: %q", calls)
			}
		}
	}
	if len(outputs) != 2 || outputs[0] != outputs[1] {
		t.Fatalf("LF executable output = %q; CRLF executable output = %q", outputs[0], outputs[1])
	}
}

// TestImageBuildUsesEmbeddedContextAndCleansUp proves a build receives the embedded recipe and normalized fingerprint and leaves no temporary context through the public image build API.
func TestImageBuildUsesEmbeddedContextAndCleansUp(t *testing.T) {
	var directory string
	var requestedTag string
	tag, err := image.Build(context.Background(), image.Context(), image.BuildOptions{}, func(ctx context.Context, args ...string) error {
		if len(args) != 6 || args[0] != "build" || args[1] != "--file" || args[3] != "--tag" {
			t.Fatalf("Podman argv = %q; want build --file RECIPE --tag TAG CONTEXT", args)
		}
		directory = filepath.Dir(args[5])
		requestedTag = args[4]
		if args[2] != filepath.Join(directory, "base", "Containerfile") || args[5] != filepath.Join(directory, "base") {
			t.Fatalf("recipe/context paths = %q", args)
		}
		fingerprint, err := os.ReadFile(filepath.Join(args[5], "nodesource-signing-key.fingerprint"))
		if err != nil || string(fingerprint) != expectedFingerprint {
			t.Fatalf("build fingerprint = %q, error = %v", fingerprint, err)
		}
		// Every embedded file must reach the materialized context, not only the recipe's directory.
		err = fs.WalkDir(image.Context(), ".", func(name string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			want, err := fs.ReadFile(image.Context(), name)
			if err != nil {
				return err
			}
			got, err := os.ReadFile(filepath.Join(directory, filepath.FromSlash(name)))
			if err != nil {
				return err
			}
			if string(got) != string(want) {
				t.Errorf("materialized asset %s differs from embedded bytes", name)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := image.Hash(image.Context())
	if err != nil {
		t.Fatal(err)
	}
	if tag != "localhost/sandboxed-agents:none-"+hash {
		t.Fatalf("built tag = %q; want embedded asset hash", tag)
	}
	if requestedTag != tag {
		t.Fatalf("Podman tag = %q; returned tag = %q", requestedTag, tag)
	}
	if directory == "" {
		t.Fatal("Podman build was never invoked")
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("temporary context remains after build: %v", err)
	}
}

// TestImageBuildCleansUpAfterFailure proves failed and cancelled Podman builds retain their cause, return no tag, and remove their context through the public image build API.
func TestImageBuildCleansUpAfterFailure(t *testing.T) {
	for _, cause := range []error{errors.New("Podman build failed"), context.Canceled} {
		t.Run(cause.Error(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var directory string
			tag, err := image.Build(ctx, nil, image.BuildOptions{}, func(received context.Context, args ...string) error {
				directory = filepath.Dir(args[len(args)-1])
				if _, err := os.Stat(filepath.Join(directory, "base", "Containerfile")); err != nil {
					t.Fatal(err)
				}
				if cause == context.Canceled {
					cancel()
					return received.Err()
				}
				return cause
			})
			if !errors.Is(err, cause) || tag != "" {
				t.Fatalf("tag = %q, error = %v; want no tag and %v", tag, err, cause)
			}
			if directory == "" {
				t.Fatal("Podman build was never invoked")
			}
			if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Fatalf("temporary context remains after failure: %v", err)
			}
		})
	}
}

// TestImageBuildRefusesInvalidInputsWithoutLeavingContext proves mixed none, missing recipes, invalid assets, and pre-cancelled builds leave no context or Podman call through the public build API.
func TestImageBuildRefusesInvalidInputsWithoutLeavingContext(t *testing.T) {
	for _, test := range []struct {
		name      string
		source    fs.FS
		options   image.BuildOptions
		cancelled bool
	}{
		{name: "mixed none", options: image.BuildOptions{Toolchains: []string{"none", "native"}}},
		{name: "missing recipe", source: fstest.MapFS{"base/fingerprint": {Data: []byte("fingerprint\n")}}},
		{name: "invalid asset", source: fstest.MapFS{"base/link": {Mode: fs.ModeSymlink}}},
		{name: "cancelled", cancelled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			for _, variable := range []string{"TMPDIR", "TMP", "TEMP"} {
				t.Setenv(variable, directory)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.cancelled {
				cancel()
			}
			tag, err := image.Build(ctx, test.source, test.options, func(context.Context, ...string) error {
				t.Fatal("Podman called for invalid input")
				return nil
			})
			if err == nil || tag != "" {
				t.Fatalf("tag = %q, error = %v; want failure", tag, err)
			}
			if test.cancelled && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel cause was lost: %v", err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				t.Fatalf("temporary context remains: %v, error = %v", entries, err)
			}
		})
	}
}

// TestImageBuildNormalizesFingerprintAndDerivesTags proves CRLF assets yield the LF build tag and normalized fingerprint while a changed fingerprint yields a new tag through the public build API.
func TestImageBuildNormalizesFingerprintAndDerivesTags(t *testing.T) {
	crlf := t.TempDir()
	releasetest.CopyCRLFContext(t, image.Context(), crlf)
	var tags []string
	for _, source := range []fs.FS{image.Context(), os.DirFS(crlf)} {
		tag, err := image.Build(context.Background(), source, image.BuildOptions{}, func(_ context.Context, args ...string) error {
			fingerprint, err := os.ReadFile(filepath.Join(args[len(args)-1], "nodesource-signing-key.fingerprint"))
			if err != nil || string(fingerprint) != expectedFingerprint {
				t.Fatalf("build fingerprint = %q, error = %v", fingerprint, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		tags = append(tags, tag)
	}
	if tags[0] != tags[1] {
		t.Fatalf("LF tag = %q, CRLF tag = %q", tags[0], tags[1])
	}
	if err := os.WriteFile(filepath.Join(crlf, "base", "nodesource-signing-key.fingerprint"), []byte("0000000000000000000000000000000000000000\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changedTag, err := image.Build(context.Background(), os.DirFS(crlf), image.BuildOptions{}, func(_ context.Context, args ...string) error {
		fingerprint, err := os.ReadFile(filepath.Join(args[len(args)-1], "nodesource-signing-key.fingerprint"))
		if err != nil || string(fingerprint) != "0000000000000000000000000000000000000000\n" {
			t.Fatalf("changed build fingerprint = %q, error = %v", fingerprint, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if changedTag == tags[0] {
		t.Fatal("changed fingerprint retained the previous image tag")
	}
}

// TestImageTagsIdentifyToolchainSets proves tags distinguish assets and toolchain sets and ignore ordering and duplicates through the public image tag API.
func TestImageTagsIdentifyToolchainSets(t *testing.T) {
	const hash = "a25ef61c2fbd6d4e034154492b542f255f949e8d779163f2cde86c34b54a577d"
	for _, test := range []struct {
		set  []string
		want string
	}{
		{nil, "localhost/sandboxed-agents:none-" + hash},
		{[]string{"none"}, "localhost/sandboxed-agents:none-" + hash},
		{[]string{"native"}, "localhost/sandboxed-agents:native-" + hash},
		{[]string{"playwright", "dotnet", "dotnet"}, "localhost/sandboxed-agents:dotnet-playwright-" + hash},
	} {
		before := append([]string(nil), test.set...)
		if got, err := image.Tag(hash, test.set); err != nil || got != test.want {
			t.Errorf("Tag(%q) = %q, error = %v; want %q", test.set, got, err, test.want)
		}
		if !reflect.DeepEqual(before, test.set) {
			t.Errorf("Tag changed input from %q to %q", before, test.set)
		}
	}
	original, err := image.Tag(hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := image.Tag("b25ef61c2fbd6d4e034154492b542f255f949e8d779163f2cde86c34b54a577d", nil)
	if err != nil {
		t.Fatal(err)
	}
	if original == changed {
		t.Error("different asset hashes have the same tag")
	}
}

// TestImageTagRejectsMixedNone proves none cannot be combined with other toolchains through the public image tag API.
func TestImageTagRejectsMixedNone(t *testing.T) {
	const hash = "a25ef61c2fbd6d4e034154492b542f255f949e8d779163f2cde86c34b54a577d"
	for _, set := range [][]string{{"none", "dotnet"}, {"native", "none"}, {"none", "none", "azure"}} {
		tag, err := image.Tag(hash, set)
		if tag != "" || err == nil || err.Error() != `toolchain "none" must stand alone` {
			t.Errorf("Tag(%q) = %q, error = %v; want no tag and none-mixing error", set, tag, err)
		}
	}
}
