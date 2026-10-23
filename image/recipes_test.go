package image_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/image"
)

// TestBaseRecipeSuppliesRuntimeContents proves the agreed runtime, the T3 Code Tool's runtime library and Tailscale package behind its fingerprint-checked repository, agent identity, manager and version manifest are installed without baked SSH host keys or a start of tailscaled through the embedded recipe seam.
func TestBaseRecipeSuppliesRuntimeContents(t *testing.T) {
	data, err := fs.ReadFile(image.Context(), "base/Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	recipe := string(data)
	for _, required := range []string{
		"FROM debian:trixie-slim", "openssh-server", "openssh-client", "git", "gh", "tmux", "curl", "jq", "ripgrep", "less", "unzip", "ca-certificates",
		"groupadd --gid 1000 agent", "useradd --uid 1000 --gid 1000", "rm -f /etc/ssh/ssh_host_*",
		"COPY --chmod=0755 manager.bin /usr/local/bin/sandboxed-agents-manager",
		"/usr/local/share/sandboxed-agents/versions.txt", "dpkg-query", "node --version", "npm --version",
		"libatomic1", "pkgs.tailscale.com/stable/debian trixie main", "apt-get install -y --no-install-recommends tailscale", "tailscale-signing-key.fingerprint Tailscale",
	} {
		if !strings.Contains(recipe, required) {
			t.Errorf("base recipe is missing %q", required)
		}
	}
	for _, toolchain := range []string{"dotnet-sdk", "azure-cli", "playwright install", "build-essential", "npm install -g"} {
		if strings.Contains(recipe, toolchain) {
			t.Errorf("base recipe unexpectedly installs %q", toolchain)
		}
	}
	// The manager changes with every executable; the package layers above it stay cached across those changes.
	manager, node, tailscale := strings.Index(recipe, "COPY --chmod=0755 manager.bin"), strings.Index(recipe, "deb.nodesource.com/node_24.x"), strings.Index(recipe, "pkgs.tailscale.com/stable/debian")
	if manager < 0 || node < 0 || tailscale < 0 {
		t.Fatalf("base recipe anchors missing: manager=%d node=%d tailscale=%d", manager, node, tailscale)
	}
	if manager < node || manager < tailscale {
		t.Error("base recipe copies the manager before the package layers, so a manager change invalidates them")
	}
	// The sandbox never starts tailscaled at container start: the manager starts it as agent for the T3 Code Tool only.
	if strings.Contains(recipe, "tailscaled") {
		t.Error("base recipe mentions tailscaled; the tailnet node is started by the manager, not the image")
	}
}

// TestSelectedToolchainsLayerOnBase proves each delivered toolchain and a combined set receive a canonical selected recipe layered on the base image through the image build boundary.
func TestSelectedToolchainsLayerOnBase(t *testing.T) {
	for _, set := range [][]string{{"dotnet"}, {"playwright"}, {"azure"}, {"native"}, {"playwright", "dotnet", "azure", "native", "dotnet"}} {
		t.Run(strings.Join(set, ","), func(t *testing.T) {
			hash, err := image.Hash(image.Context())
			if err != nil {
				t.Fatal(err)
			}
			tag, err := image.BuildWithToolchains(context.Background(), nil, set, func(_ context.Context, args ...string) error {
				data, err := os.ReadFile(args[2])
				if err != nil {
					t.Fatal(err)
				}
				recipe := string(data)
				if !strings.HasPrefix(recipe, "ARG BASE_IMAGE=localhost/sandboxed-agents:none-"+hash+"\nFROM ${BASE_IMAGE}\n") {
					t.Fatalf("selected recipe does not layer on base: %s", recipe)
				}
				if filepath.Base(args[5]) == "base" {
					t.Fatal("toolchain context must include the selected fragments")
				}
				for name, install := range map[string]string{"dotnet": "dotnet-sdk-10.0", "playwright": "playwright install --with-deps chromium firefox webkit", "azure": "azure-cli python3-azext-devops", "native": "build-essential cmake ninja-build pkg-config gdb"} {
					selected := false
					for _, entry := range set {
						selected = selected || name == entry
					}
					if got := strings.Count(recipe, install); got != 0 && !selected || selected && got != 1 {
						t.Errorf("%s install count = %d, selected = %t", name, got, selected)
					}
				}
				if !strings.Contains(recipe, "record-versions.sh") {
					t.Fatal("toolchain recipe does not refresh versions manifest")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if tag != image.Tag(hash, set) {
				t.Fatalf("tag = %q", tag)
			}
		})
	}
}
