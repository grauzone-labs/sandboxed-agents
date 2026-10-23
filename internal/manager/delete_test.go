package manager_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// gitIn runs Git in a directory for the fixture and fails the test on an error.
func gitIn(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = testGitEnvironment()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, directory, err, output)
	}
	return strings.TrimSpace(string(output))
}

// testGitEnvironment isolates Git from the host's configuration and gives it an identity for commits.
func testGitEnvironment() []string {
	return append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid")
}

// deleteFixture is an enabled T3 Code beside agents, credentials, integration configuration, a workspace repository, and user projects inside and outside the Tool directory.
type deleteFixture struct {
	// home is the test home; workspace is the user's repository outside it.
	home, workspace string
	// insideWorktree is a linked worktree of the workspace repository that T3 Code created under its own home, `home/`.
	insideWorktree string
	// insideRepository is a repository created in the app under T3 Code's own home, with its own linked worktree beside it.
	insideRepository, insideRepositoryWorktree string
	// outsideWorktree is a linked worktree of the workspace repository outside the Tool directory.
	outsideWorktree string
}

// newDeleteFixture builds the fixture home, enables T3 Code with a running service and a login, and returns the fixture with the effects.
func newDeleteFixture(t *testing.T) (deleteFixture, *toolRun, manager.Effects, *fakeServices) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the relocation of user projects needs git on PATH")
	}
	home, run, e, services := enabledTool(t)
	for _, file := range []string{".local/share/sandboxed-agents/selection.json", ".local/share/sandboxed-agents/installation-codex/bin/codex", ".local/share/sandboxed-agents/installation-codex/lib/node_modules/@openai/codex/package.json", ".npm/_cacache/index", ".gitconfig", "notes.txt"} {
		path := filepath.Join(home, file)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("preserve "+file), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(home, ".local", "share", "sandboxed-agents", "installation-codex", "bin", "codex"), filepath.Join(home, ".local", "bin", "codex")); err != nil {
		t.Fatal(err)
	}
	runTools(t, e, 0, "connect", "t3code", "login")
	f := deleteFixture{home: home, workspace: filepath.Join(t.TempDir(), "workspace")}
	if err := os.MkdirAll(f.workspace, 0700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.workspace, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(f.workspace, "README.md"), []byte("workspace\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.workspace, "add", "README.md")
	gitIn(t, f.workspace, "commit", "-q", "-m", "initial")
	toolHome := filepath.Join(toolDirectory(home), "home")
	f.insideWorktree = filepath.Join(toolHome, "worktrees", "workspace", "feature-inside")
	gitIn(t, f.workspace, "worktree", "add", "-q", "-b", "feature-inside", f.insideWorktree)
	f.outsideWorktree = filepath.Join(home, "worktrees", "workspace", "feature-outside")
	gitIn(t, f.workspace, "worktree", "add", "-q", "-b", "feature-outside", f.outsideWorktree)
	f.insideRepository = filepath.Join(toolHome, "projects", "created-in-app")
	if err := os.MkdirAll(f.insideRepository, 0700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.insideRepository, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(f.insideRepository, "app.txt"), []byte("created in the app\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.insideRepository, "add", "app.txt")
	gitIn(t, f.insideRepository, "commit", "-q", "-m", "initial")
	f.insideRepositoryWorktree = filepath.Join(toolHome, "worktrees", "created-in-app", "topic")
	gitIn(t, f.insideRepository, "worktree", "add", "-q", "-b", "topic", f.insideRepositoryWorktree)
	for _, file := range []string{"home/userdata/service.env", "home/userdata/statev2.sqlite", "home/userdata/logs/server.trace.ndjson", "home/caches/codex.json", "home/tools/cloudflared/2026.10.0/linux-x64/cloudflared", "home/runtime/versions/0.1.2/t3"} {
		path := filepath.Join(toolDirectory(home), file)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("tool-owned "+file), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return f, run, e, services
}

// assertUsableWithGit proves a moved project still answers Git and is listed by its repository.
func assertUsableWithGit(t *testing.T, repository, project string) {
	t.Helper()
	gitIn(t, project, "status", "--short")
	if list := gitIn(t, repository, "worktree", "list", "--porcelain"); !strings.Contains(list, "worktree "+project+"\n") {
		t.Fatalf("%s is not listed by %s:\n%s", project, repository, list)
	}
}

// TestToolsDeleteRemovesEverythingAndKeepsUserFiles proves delete stops the service, signs out, moves every repository and linked worktree out of the Tool directory with Git so that they stay usable, removes the Tool directory and the command link, names the moved paths, keeps every other file byte for byte, and that a second delete finds nothing and exits zero, at RunWithEffects with real Git and a fake service system.
func TestToolsDeleteRemovesEverythingAndKeepsUserFiles(t *testing.T) {
	f, run, e, services := newDeleteFixture(t)
	before := homeSnapshot(t, f.home)
	// The command link is Tool-owned and goes with the Tool; it is not part of the files that must survive.
	delete(before, filepath.Join(f.home, ".local", "bin", "t3"))
	out := runTools(t, e, 0, "delete", "t3code")
	moved := map[string]string{
		f.insideWorktree:           filepath.Join(f.home, "t3code-files", "worktrees", "workspace", "feature-inside"),
		f.insideRepository:         filepath.Join(f.home, "t3code-files", "projects", "created-in-app"),
		f.insideRepositoryWorktree: filepath.Join(f.home, "t3code-files", "worktrees", "created-in-app", "topic"),
	}
	for from, to := range moved {
		if !strings.Contains(out, "moved the") || !strings.Contains(out, from+" to "+to) {
			t.Fatalf("output does not name the move %s to %s: %s", from, to, out)
		}
	}
	if !strings.Contains(out, "t3code deleted") {
		t.Fatalf("output=%s", out)
	}
	assertUsableWithGit(t, f.workspace, moved[f.insideWorktree])
	assertUsableWithGit(t, f.workspace, f.outsideWorktree)
	assertUsableWithGit(t, moved[f.insideRepository], moved[f.insideRepository])
	assertUsableWithGit(t, moved[f.insideRepository], moved[f.insideRepositoryWorktree])
	if _, err := os.Lstat(toolDirectory(f.home)); !os.IsNotExist(err) {
		t.Fatalf("Tool directory remains: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(f.home, ".local", "bin", "t3")); !os.IsNotExist(err) {
		t.Fatalf("command link remains: %v", err)
	}
	if services.runningServices() != 0 {
		t.Fatalf("service still running after the delete")
	}
	if len(run.connectCalls) == 0 || run.connectCalls[len(run.connectCalls)-1][1] != "logout" {
		t.Fatalf("logout not attempted: %v", run.connectCalls)
	}
	after := homeSnapshot(t, f.home)
	for path, content := range before {
		if after[path] != content {
			t.Fatalf("file changed by the delete: %s", path)
		}
	}
	for path := range after {
		if _, existed := before[path]; !existed && !strings.HasPrefix(path, filepath.Join(f.home, "t3code-files")) {
			t.Fatalf("delete created %s", path)
		}
	}
	if out := runTools(t, e, 0, "delete", "t3code"); !strings.Contains(out, "t3code not enabled; nothing to delete") {
		t.Fatalf("second delete output=%s", out)
	}
	supervisors := len(services.detachArgs)
	runTools(t, e, 0, "restore")
	if len(services.detachArgs) != supervisors {
		t.Fatalf("restore after the delete started a service: %v", services.detachArgs)
	}
	if out := runTools(t, e, 0, "enable", "t3code"); !strings.Contains(out, "version 0.1.2 (channel latest); service running") {
		t.Fatalf("enable after delete=%s", out)
	}
	status := readFullToolStatus(t, e)
	if status.Pin != "" || status.Channel != "latest" || status.Connect == nil || status.Connect.Authenticated || status.Connect.Linked {
		t.Fatalf("enable after delete did not start from scratch: %+v connect=%+v", status, status.Connect)
	}
	if _, err := os.Stat(credentialPath(f.home)); !os.IsNotExist(err) {
		t.Fatalf("credential survived the delete: %v", err)
	}
}

// TestToolsDeleteRefusesBeforeRemovingAnything proves a target that exists, a worktree whose repository cannot be found, a repository among the Tool's own files, and a command link the Tool does not own each make the delete refuse before the service stops or any file, Tool-owned or not, changes, and that a login in progress or another change holding the manager lock refuse the delete, at RunWithEffects with real Git.
func TestToolsDeleteRefusesBeforeRemovingAnything(t *testing.T) {
	f, run, e, services := newDeleteFixture(t)
	before, toolBefore := homeSnapshot(t, f.home), treeSnapshot(t, toolDirectory(f.home))
	unchanged := func(name string) {
		t.Helper()
		if services.runningServices() != 1 {
			t.Fatalf("%s: service stopped", name)
		}
		if _, err := os.Stat(credentialPath(f.home)); err != nil {
			t.Fatalf("%s: credential removed: %v", name, err)
		}
		if after := homeSnapshot(t, f.home); !equalSnapshots(before, after) {
			t.Fatalf("%s: files changed", name)
		}
		if after := treeSnapshot(t, toolDirectory(f.home)); !equalSnapshots(toolBefore, after) {
			t.Fatalf("%s: Tool files changed", name)
		}
	}
	for _, misplaced := range []string{filepath.Join(toolDirectory(f.home), "home", "runtime", "versions", "0.1.2", "node_modules", "pkg"), filepath.Join(toolDirectory(f.home), "home", "userdata", "attachments"), filepath.Join(toolDirectory(f.home), "npm", "0.1.2", "lib"), filepath.Join(toolDirectory(f.home), "home")} {
		if err := os.MkdirAll(filepath.Join(misplaced, ".git"), 0700); err != nil {
			t.Fatal(err)
		}
		before, toolBefore = homeSnapshot(t, f.home), treeSnapshot(t, toolDirectory(f.home))
		if out := runTools(t, e, 1, "delete", "t3code"); !strings.Contains(out, "lies among the Tool's own files") {
			t.Fatalf("misplaced repository %s output=%s", misplaced, out)
		}
		unchanged("misplaced repository")
		if err := os.RemoveAll(filepath.Join(misplaced, ".git")); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(f.home, ".local", "bin", "t3")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/usr/bin/true", link); err != nil {
		t.Fatal(err)
	}
	before, toolBefore = homeSnapshot(t, f.home), treeSnapshot(t, toolDirectory(f.home))
	if out := runTools(t, e, 1, "delete", "t3code"); !strings.Contains(out, "refusing unmanaged command") {
		t.Fatalf("foreign command link output=%s", out)
	}
	unchanged("foreign command link")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	before, toolBefore = homeSnapshot(t, f.home), treeSnapshot(t, toolDirectory(f.home))
	if out := runTools(t, e, 1, "delete", "t3code"); !strings.Contains(out, "refusing unmanaged command") {
		t.Fatalf("regular command file output=%s", out)
	}
	unchanged("regular command file")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(toolDirectory(f.home), "npm", "0.1.2", "bin", "t3"), link); err != nil {
		t.Fatal(err)
	}
	before, toolBefore = homeSnapshot(t, f.home), treeSnapshot(t, toolDirectory(f.home))
	target := filepath.Join(f.home, "t3code-files", "worktrees", "workspace", "feature-inside")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	if out := runTools(t, e, 1, "delete", "t3code"); !strings.Contains(out, "which exists; move it aside first") {
		t.Fatalf("existing target output=%s", out)
	}
	unchanged("existing target")
	if err := os.RemoveAll(filepath.Join(f.home, "t3code-files")); err != nil {
		t.Fatal(err)
	}
	gitFile := filepath.Join(f.insideWorktree, ".git")
	original, err := os.ReadFile(gitFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitFile, []byte("gitdir: /nonexistent/.git/worktrees/gone\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before, toolBefore = homeSnapshot(t, f.home), treeSnapshot(t, toolDirectory(f.home))
	if out := runTools(t, e, 1, "delete", "t3code"); !strings.Contains(out, "cannot find the repository of the worktree") {
		t.Fatalf("broken worktree output=%s", out)
	}
	unchanged("broken worktree")
	if err := os.WriteFile(gitFile, original, 0600); err != nil {
		t.Fatal(err)
	}
	before, toolBefore = homeSnapshot(t, f.home), treeSnapshot(t, toolDirectory(f.home))
	release, held, err := lockConnect(f.home)
	if err != nil || !held {
		t.Fatal(err)
	}
	if out := runTools(t, e, 1, "delete", "t3code"); !strings.Contains(out, "a T3 Connect login is in progress") {
		t.Fatalf("delete during a login=%s", out)
	}
	release()
	unchanged("login in progress")
	unlock, locked, err := lockFor(t, f.home)
	if err != nil || !locked {
		t.Fatal(err)
	}
	out := runTools(t, e, 1, "delete", "t3code")
	unlock()
	if !strings.Contains(out, "another agent or tool change is in progress; retry") {
		t.Fatalf("delete under the manager lock=%s", out)
	}
	unchanged("manager lock")
	if len(run.connectCalls) != 1 {
		t.Fatalf("refused deletes ran upstream: %v", run.connectCalls)
	}
}

// TestToolsDeleteReportsInterruptedStepsAndCompletesOnRerun proves a stop that fails, a relocation that fails, and a logout that fails each report what remains or warn, that only the logout failure lets the delete finish, and that a rerun after the cause is gone completes the delete, at RunWithEffects with real Git.
func TestToolsDeleteReportsInterruptedStepsAndCompletesOnRerun(t *testing.T) {
	f, run, e, services := newDeleteFixture(t)
	// The relay refuses the logout throughout; the local credentials go with the Tool anyway.
	run.connectResults = map[string]error{"logout": childExit(1)}
	unavailable := e
	unavailable.Signal = nil
	if out := runTools(t, unavailable, 1, "delete", "t3code"); !strings.Contains(out, "t3code not deleted; the service did not stop") || !strings.Contains(out, "run tools delete again") {
		t.Fatalf("failed stop output=%s", out)
	}
	if _, err := os.Stat(toolDirectory(f.home)); err != nil || services.runningServices() != 1 {
		t.Fatalf("failed stop removed files or the service: %v running=%d", err, services.runningServices())
	}
	if os.Getuid() != 0 {
		// A user files root that cannot be written makes the relocation fail after the service stopped.
		root := filepath.Join(f.home, "t3code-files")
		if err := os.MkdirAll(root, 0500); err != nil {
			t.Fatal(err)
		}
		out := runTools(t, e, 1, "delete", "t3code")
		if !strings.Contains(out, "t3code not deleted; cannot prepare") || !strings.Contains(out, "the Tool's files remain") {
			t.Fatalf("failed relocation output=%s", out)
		}
		if _, err := os.Stat(toolDirectory(f.home)); err != nil {
			t.Fatalf("failed relocation removed the Tool directory: %v", err)
		}
		if _, err := os.Stat(f.insideWorktree); err != nil {
			t.Fatalf("failed relocation lost the worktree: %v", err)
		}
		if err := os.Chmod(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	out := runTools(t, e, 0, "delete", "t3code")
	if !strings.Contains(out, "warning: t3code connect logout failed") || !strings.Contains(out, "t3code deleted") {
		t.Fatalf("logout failure output=%s", out)
	}
	if _, err := os.Lstat(toolDirectory(f.home)); !os.IsNotExist(err) {
		t.Fatalf("Tool directory remains after the rerun: %v", err)
	}
	assertUsableWithGit(t, f.workspace, filepath.Join(f.home, "t3code-files", "worktrees", "workspace", "feature-inside"))
}

// treeSnapshot records every regular file below a directory with its content, following no symlink.
func treeSnapshot(t *testing.T, directory string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.Walk(directory, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[path] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// TestToolsDeleteCompletesAPartialRelocationOnRerun proves a relocation that fails after some projects moved reports the failure, keeps the Tool's files, and that a rerun moves the rest, repairs the links of the earlier moves, and completes the delete with every project usable with Git, at RunWithEffects with real Git.
func TestToolsDeleteCompletesAPartialRelocationOnRerun(t *testing.T) {
	f, _, e, _ := newDeleteFixture(t)
	// The worktrees move first; a regular file where the repositories' parent belongs fails the repository's move.
	blocker := filepath.Join(f.home, "t3code-files", "projects")
	if err := os.MkdirAll(filepath.Dir(blocker), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, []byte("in the way"), 0600); err != nil {
		t.Fatal(err)
	}
	out := runTools(t, e, 1, "delete", "t3code")
	movedWorktree := filepath.Join(f.home, "t3code-files", "worktrees", "created-in-app", "topic")
	if !strings.Contains(out, "moved the worktree "+f.insideRepositoryWorktree+" to "+movedWorktree) || !strings.Contains(out, "cannot prepare "+filepath.Join(blocker, "created-in-app")) || !strings.Contains(out, "the Tool's files remain") {
		t.Fatalf("partial relocation output=%s", out)
	}
	if _, err := os.Stat(f.insideRepository); err != nil {
		t.Fatalf("repository removed by a failed relocation: %v", err)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if out := runTools(t, e, 0, "delete", "t3code"); !strings.Contains(out, "t3code deleted") {
		t.Fatalf("rerun output=%s", out)
	}
	movedRepository := filepath.Join(f.home, "t3code-files", "projects", "created-in-app")
	assertUsableWithGit(t, movedRepository, movedRepository)
	assertUsableWithGit(t, movedRepository, movedWorktree)
	assertUsableWithGit(t, f.workspace, filepath.Join(f.home, "t3code-files", "worktrees", "workspace", "feature-inside"))
	if _, err := os.Lstat(toolDirectory(f.home)); !os.IsNotExist(err) {
		t.Fatalf("Tool directory remains: %v", err)
	}
}

// TestToolsDeleteReportsFailedRemovalsAndCompletesOnRerun proves a command link that cannot be removed and a Tool directory that cannot be removed each report what remains and exit non-zero after the projects moved, and that a rerun completes the delete, at RunWithEffects with real Git.
func TestToolsDeleteReportsFailedRemovalsAndCompletesOnRerun(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("read-only directories do not stop root")
	}
	f, _, e, _ := newDeleteFixture(t)
	bin := filepath.Join(f.home, ".local", "bin")
	if err := os.Chmod(bin, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(bin, 0700) })
	if out := runTools(t, e, 1, "delete", "t3code"); !strings.Contains(out, "cannot remove the command link") || !strings.Contains(out, "run tools delete again") || !strings.Contains(out, "moved the worktree "+f.insideWorktree) {
		t.Fatalf("failed link removal output=%s", out)
	}
	if _, err := os.Lstat(f.insideWorktree); !os.IsNotExist(err) {
		t.Fatalf("worktree not moved before the link failure: %v", err)
	}
	if _, err := os.Stat(toolDirectory(f.home)); err != nil {
		t.Fatalf("Tool directory removed although the link remained: %v", err)
	}
	if err := os.Chmod(bin, 0700); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(toolDirectory(f.home))
	if err := os.Chmod(parent, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0700) })
	if out := runTools(t, e, 1, "delete", "t3code"); !strings.Contains(out, "the Tool directory could not be removed") {
		t.Fatalf("failed directory removal output=%s", out)
	}
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if out := runTools(t, e, 0, "delete", "t3code"); !strings.Contains(out, "t3code deleted") {
		t.Fatalf("rerun output=%s", out)
	}
	if _, err := os.Lstat(toolDirectory(f.home)); !os.IsNotExist(err) {
		t.Fatalf("Tool directory remains: %v", err)
	}
	assertUsableWithGit(t, f.workspace, filepath.Join(f.home, "t3code-files", "worktrees", "workspace", "feature-inside"))
}
