package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestWorkspaceRejectsWindowsDefaultInstallDirectory proves validateWorkspaceForHost and public Parse reject the default per-user Windows install directory and overlapping paths while allowing an unrelated sibling, even when the running executable is elsewhere.
func TestWorkspaceRejectsWindowsDefaultInstallDirectory(t *testing.T) {
	localAppData := t.TempDir()
	t.Setenv("LOCALAPPDATA", localAppData)
	programs := filepath.Join(localAppData, "Programs")
	install := filepath.Join(programs, "sandboxed-agents")
	descendant := filepath.Join(install, "workspace")
	sibling := filepath.Join(programs, "project")
	for _, directory := range []string{descendant, sibling} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, fixture := range []struct {
		name, path string
		rejected   bool
	}{
		{"directory", install, true},
		{"ancestor", programs, true},
		{"descendant", descendant, true},
		{"sibling", sibling, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			err := validateWorkspaceForHost(fixture.path, "windows")
			if fixture.rejected {
				if err == nil || !strings.Contains(err.Error(), "workspace overlaps protected host path") {
					t.Fatalf("workspace %s: error=%v; want protected host path refusal", fixture.path, err)
				}
			} else if err != nil {
				t.Fatalf("unrelated workspace %s: %v", fixture.path, err)
			}
		})
	}
	t.Run("alias", func(t *testing.T) {
		alias := filepath.Join(t.TempDir(), "install-alias")
		if err := os.Symlink(install, alias); err != nil {
			t.Skipf("directory symlink unavailable: %v", err)
		}
		if err := validateWorkspaceForHost(alias, "windows"); err == nil || !strings.Contains(err.Error(), "workspace overlaps protected host path") {
			t.Fatalf("aliased install directory: error=%v; want protected host path refusal", err)
		}
	})
	t.Run("not installed", func(t *testing.T) {
		localAppData := t.TempDir()
		t.Setenv("LOCALAPPDATA", localAppData)
		programs := filepath.Join(localAppData, "Programs")
		if err := os.Mkdir(programs, 0700); err != nil {
			t.Fatal(err)
		}
		if err := validateWorkspaceForHost(programs, "windows"); err == nil || !strings.Contains(err.Error(), "workspace overlaps protected host path") {
			t.Fatalf("ancestor of not-yet-created install directory: error=%v; want protected host path refusal", err)
		}
	})
	t.Run("running executable", func(t *testing.T) {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		if err := validateWorkspaceForHost(filepath.Dir(executable), "windows"); err == nil || !strings.Contains(err.Error(), "workspace overlaps protected host path") {
			t.Fatalf("directory of running executable: error=%v; want protected host path refusal", err)
		}
	})
	t.Run("Linux directory", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		if err := validateWorkspaceForHost(install, "linux"); err != nil {
			t.Fatalf("Windows-only default directory on Linux: %v", err)
		}
	})
	t.Run("Windows Parse", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("public Parse Windows path selection requires Windows")
		}
		_, err := Parse([]string{"up", "demo", install, "--with", "none"}, "default")
		if err == nil || !strings.Contains(err.Error(), "workspace overlaps protected host path") {
			t.Fatalf("Windows Parse install directory: error=%v; want protected host path refusal", err)
		}
	})
}

// TestWindowsWorkspacePathTranslation proves resolved Windows drive paths translate to the default WSL automount tree and unsupported network/relative paths fail at the named path-translation check.
func TestWindowsWorkspacePathTranslation(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{`C:\projects\demo`, `/mnt/c/projects/demo`},
		{`D:\project with spaces`, `/mnt/d/project with spaces`},
		{`z:/project,with,commas`, `/mnt/z/project,with,commas`},
		{`C:\`, `/mnt/c/`},
		{`\\?\C:\projects\demo`, `/mnt/c/projects/demo`},
		{`\\server\share\project`, ``},
		{`\\?\UNC\server\share\project`, ``},
		{`C:project`, ``},
		{`project`, ``},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := windowsWorkspacePath(tc.input)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("unsupported Windows workspace %q translated to %q", tc.input, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Windows workspace %q: got=%q error=%v; want=%q", tc.input, got, err, tc.want)
			}
		})
	}
}

// TestWindowsFinalPathNormalization proves handle-resolved Windows drive and UNC paths lose device prefixes while unsupported device paths fail at the final-path normalization seam.
func TestWindowsFinalPathNormalization(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{`\\?\C:\Users\agent\workspace`, `C:\Users\agent\workspace`},
		{`\\?\d:\workspace`, `d:\workspace`},
		{`\\?\UNC\server\share\workspace`, `\\server\share\workspace`},
		{`C:\workspace`, `C:\workspace`},
		{`\\server\share\workspace`, `\\server\share\workspace`},
		{`\\?\Volume{unknown}\workspace`, ``},
		{`relative`, ``},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := normalizeWindowsFinalPath(tc.input)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("unsupported final path %q accepted as %q", tc.input, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("final path=%q error=%v; want=%q", got, err, tc.want)
			}
		})
	}
}

// TestNonWindowsResolvedPathFollowsAliasesAndMissingAncestors proves symlink aliases and missing descendants retain canonical native paths through resolveExistingPath and resolvedPath on non-Windows.
func TestNonWindowsResolvedPathFollowsAliasesAndMissingAncestors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-Windows resolver check")
	}
	target := t.TempDir()
	alias := filepath.Join(t.TempDir(), "workspace-alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	got, err := resolveExistingPath(alias)
	if err != nil || got != target {
		t.Fatalf("existing alias=%q error=%v; want=%q", got, err, target)
	}
	child := filepath.Join(alias, "not-created", "workspace")
	got, err = resolvedPath(child)
	want := filepath.Join(target, "not-created", "workspace")
	if err != nil || got != want {
		t.Fatalf("missing child=%q error=%v; want=%q", got, err, want)
	}
}
