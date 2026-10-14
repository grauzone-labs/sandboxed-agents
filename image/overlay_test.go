package image_test

import (
	"bytes"
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/releasetest"
)

// TestOverlayEmbedsStaticLinuxManager proves the public overlay build command embeds the same static Linux amd64 manager from LF and CRLF checkouts in a host executable.
func TestOverlayEmbedsStaticLinuxManager(t *testing.T) {
	var previous []byte
	for _, endings := range []string{"LF", "CRLF"} {
		checkout := filepath.Join(t.TempDir(), endings)
		releasetest.CopyRepositorySources(t, "..", checkout)
		releasetest.CopyCRLFContext(t, os.DirFS("context"), filepath.Join(checkout, "image", "context"))
		if endings == "CRLF" {
			err := filepath.WalkDir(checkout, func(name string, entry os.DirEntry, err error) error {
				if err != nil || entry.IsDir() || filepath.Ext(name) != ".go" {
					return err
				}
				relative, err := filepath.Rel(checkout, name)
				if err != nil {
					return err
				}
				return releasetest.RewriteFileToCRLF(checkout, relative)
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		overlay := t.TempDir()
		command := exec.Command("go", "run", "./image/cmd/overlay", overlay)
		command.Dir = checkout
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("overlay: %v\n%s", err, output)
		}
		manager, err := os.ReadFile(filepath.Join(overlay, "context", "base", "manager.bin"))
		if err != nil {
			t.Fatal(err)
		}
		binary, err := elf.NewFile(bytes.NewReader(manager))
		if err != nil {
			t.Fatalf("embedded manager is not ELF: %v", err)
		}
		if binary.Machine != elf.EM_X86_64 {
			t.Fatalf("manager architecture = %v", binary.Machine)
		}
		for _, program := range binary.Progs {
			if program.Type == elf.PT_INTERP {
				t.Fatal("manager requires a dynamic interpreter")
			}
		}
		if previous != nil && !bytes.Equal(previous, manager) {
			t.Fatal("manager changes with checkout line endings or directory")
		}
		previous = manager
		name := "embedded-manager"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		driver := filepath.Join(t.TempDir(), name)
		command = exec.Command("go", "build", "-overlay", filepath.Join(overlay, "overlay.json"), "-o", driver, "./image/testdata/manager")
		command.Dir = checkout
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("host build: %v\n%s", err, output)
		}
		command = exec.Command(driver)
		if output, err := command.Output(); err != nil || !bytes.Equal(output, manager) {
			t.Fatalf("host embedded manager differs, error = %v", err)
		}
	}
}
