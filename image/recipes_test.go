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
	assertRecipeFragments(t, recipe, []string{
		"FROM debian:trixie-slim", "openssh-server", "openssh-client", "git", "gh", "tmux", "curl", "jq", "ripgrep", "less", "unzip", "ca-certificates",
		"groupadd --gid 1000 agent", "useradd --uid 1000 --gid 1000", "rm -f /etc/ssh/ssh_host_*",
		"COPY --chmod=0755 manager.bin /usr/local/bin/sandboxed-agents-manager",
		"mkdir -p /usr/local/share/sandboxed-agents", "/usr/local/bin/sandboxed-agents-manager record-versions",
		"libatomic1", "pkgs.tailscale.com/stable/debian trixie main", "apt-get install -y --no-install-recommends tailscale", "tailscale-signing-key.fingerprint Tailscale",
	}, []string{"dotnet-sdk", "azure-cli", "playwright install", "build-essential", "npm install -g"})
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
				for name, install := range map[string]string{"dotnet": "dotnet-sdk-10.0", "playwright": "playwright install --with-deps chromium firefox webkit", "azure": "azure-cli/bookworm", "native": "build-essential cmake ninja-build pkg-config gdb"} {
					selected := false
					for _, entry := range set {
						selected = selected || name == entry
					}
					if got := strings.Count(recipe, install); got != 0 && !selected || selected && got != 1 {
						t.Errorf("%s install count = %d, selected = %t", name, got, selected)
					}
				}
				if !strings.HasSuffix(recipe, "\nRUN /usr/local/bin/sandboxed-agents-manager record-versions\n") {
					t.Fatal("toolchain recipe does not refresh the versions manifest last")
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

// TestAzureRecipeVerifiesMicrosoftDistribution proves the embedded azure recipe verifies Microsoft's key and extension checksum before noninteractive installation and keeps the extension outside home data.
func TestAzureRecipeVerifiesMicrosoftDistribution(t *testing.T) {
	for name, expected := range map[string]string{
		"toolchains/microsoft-signing-key.fingerprint": "BC528686B50D79E339D3721CEB3E94ADBE1229CF\n",
		"toolchains/azure-devops.sha256":               "cb38021f76cf93905f759c9d1b4b441bdeecfc98d4141ce26dd7b299af1aa56e  azure_devops-1.0.10-py2.py3-none-any.whl\n",
	} {
		data, err := fs.ReadFile(image.Context(), name)
		if err != nil || string(data) != expected {
			t.Fatalf("embedded %s = %q, error = %v; want %q", name, data, err, expected)
		}
	}
	data, err := fs.ReadFile(image.Context(), "toolchains/azure.Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	recipe := string(data)
	assertRecipeFragments(t, recipe, []string{
		"https://packages.microsoft.com/keys/microsoft.asc",
		"microsoft-signing-key.fingerprint Microsoft",
		"signed-by=/etc/apt/keyrings/azure-cli-microsoft.gpg",
		"https://packages.microsoft.com/repos/azure-cli/ bookworm main",
		"apt-get install -y --no-install-recommends azure-cli/bookworm",
		"https://azcliprod.blob.core.windows.net/cli-extensions/azure_devops-1.0.10-py2.py3-none-any.whl",
		"sha256sum -c", "PIP_NO_DEPS=1 PIP_NO_INDEX=1 az extension add --system --yes --source",
		"az extension show --name azure-devops --query path --output tsv",
		"toolchains/azure-devops-requirements.txt", "--require-hashes --only-binary=:all:",
		"--index-url https://pypi.org/simple", "--target \"$extension_path\"",
		"USER 1000:1000\nRUN check-azure-credentials\nUSER root",
		"chmod -R a+rX \"$extension_path\"", "az devops --help", "/root/.azure",
	}, []string{"AZURE_EXTENSION_SYS_DIR", "python3-azext-devops", "/home/agent", "trusted=yes", "--allow-unauthenticated"})
	last := -1
	for _, step := range []string{"microsoft-signing-key.fingerprint Microsoft", "gpg --batch --yes --dearmor", "sources.list.d/azure-cli.list", "apt-get install", "sha256sum -c", "az extension add"} {
		next := strings.Index(recipe, step)
		if next <= last {
			t.Fatalf("installation step %q missing or out of order", step)
		}
		last = next
	}
}

// assertRecipeFragments checks the required and forbidden contents of an embedded image recipe.
func assertRecipeFragments(t *testing.T, recipe string, required, forbidden []string) {
	t.Helper()
	for _, fragment := range required {
		if !strings.Contains(recipe, fragment) {
			t.Errorf("recipe is missing %q", fragment)
		}
	}
	for _, fragment := range forbidden {
		if strings.Contains(recipe, fragment) {
			t.Errorf("recipe contains forbidden fragment %q", fragment)
		}
	}
}
