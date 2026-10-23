package manager

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// userFilesRoot is the directory in the home data that keeps the user project files a delete relocates out of a Tool directory (ADR-0009, D4).
const userFilesRoot = "t3code-files"

// toolOwnedSubtrees are the subdirectories of a Tool's own home that hold Tool files only; a Git
// repository found inside one is a false positive, which refuses the delete instead of moving or removing it.
var toolOwnedSubtrees = []string{"runtime", "tools", "caches", "userdata"}

// userProject is a Git repository or linked worktree found inside a Tool directory, with the place it moves to.
type userProject struct {
	path, target string
	// linked is true for a linked worktree, whose `.git` is a file; false for a repository, whose `.git` is a directory.
	linked bool
	// repository is the main repository a linked worktree belongs to.
	repository string
	// container is the path of the project that holds this one, empty for a top-level project; a contained project moves with its container.
	container string
}

// kind names the project for messages.
func (p userProject) kind() string {
	if p.linked {
		return "worktree"
	}
	return "repository"
}

// deleteTool removes a Tool and everything it stored, its tailnet node included, after relocating the
// user project files found inside its directory with Git's own commands, and reports what it moved and what remains.
func deleteTool(home, root string, r toolRequest, stdout, stderr io.Writer, effects Effects) error {
	directory := toolDirectory(root, r.name)
	command := filepath.Join(home, ".local", "bin", r.definition.Command)
	if _, err := os.Lstat(directory); os.IsNotExist(err) {
		fmt.Fprintf(stdout, "%s not enabled; nothing to delete\n", r.name)
		return nil
	}
	// What the pre-check can tell, an existing target, a worktree without its repository, a false positive,
	// and a command link the Tool does not own, refuses the delete before the service stops.
	projects, err := inventoryUserProjects(home, directory, effects)
	if err != nil {
		return fmt.Errorf("%s not deleted; %w", r.name, err)
	}
	if _, err := managedToolCommandLink(command, directory); err != nil {
		return fmt.Errorf("%s not deleted; %w", r.name, err)
	}
	current, enabled, err := readToolSelection(directory)
	if err != nil {
		fmt.Fprintf(stderr, "warning: the %s selection cannot be read (%v); no logout is attempted\n", r.name, err)
	}
	if err := stopSupervised(toolService(directory, r.name, current.Version, r.definition), effects); err != nil {
		return fmt.Errorf("%s not deleted; the service did not stop: %w; run tools delete again", r.name, err)
	}
	if enabled {
		credential := filepath.Join(toolOwnHome(directory), filepath.FromSlash(r.definition.CredentialPath))
		if _, err := os.Stat(credential); err == nil {
			if err := runConnectQuietly(directory, current, r.definition, "logout", effects, stderr); err != nil {
				fmt.Fprintf(stderr, "warning: %s connect logout failed: %v; the local credentials are removed anyway\n", r.name, err)
			}
		}
	}
	removed := "the service, "
	if tailnetExists(directory) {
		// The node signs out as a best effort, so that the device leaves the tailnet; its state and Serve mapping go with the directory.
		tailnetSignOut(directory, r.name, stderr, effects)
		if err := stopSupervised(tailnetNode(directory, r.name), effects); err != nil {
			return fmt.Errorf("%s not deleted; the tailnet node did not stop: %w; run tools delete again", r.name, err)
		}
		removed += "the tailnet node, "
	}
	// The service may have created project files until it stopped, so the inventory is taken again.
	if projects, err = inventoryUserProjects(home, directory, effects); err != nil {
		return fmt.Errorf("%s not deleted; %w; the Tool's files remain; run tools delete again", r.name, err)
	}
	if err := relocateUserProjects(projects, stdout, effects); err != nil {
		return fmt.Errorf("%s not deleted; %w; the Tool's files remain; run tools delete again", r.name, err)
	}
	if target, err := managedToolCommandLink(command, directory); err != nil {
		return fmt.Errorf("%s not deleted; %w; run tools delete again", r.name, err)
	} else if target != "" {
		if err := os.Remove(command); err != nil {
			return fmt.Errorf("%s not deleted; cannot remove the command link %s: %w; run tools delete again", r.name, command, err)
		}
	}
	if err := os.RemoveAll(directory); err != nil {
		return fmt.Errorf("%s not deleted; the Tool directory could not be removed: %w; run tools delete again", r.name, err)
	}
	fmt.Fprintf(stdout, "%s deleted: %sthe installation, the user data, the credentials, and the selection are removed\n", r.name, removed)
	return nil
}

// toolProcess is the launcher of the installed version, run as agent with the Tool's own home.
func toolProcess(directory string, current toolSelection, definition catalog.Tool, args []string) Process {
	return Process{Program: toolLauncher(directory, current.Version, definition), Args: args, Identity: agentIdentity, EnvironmentPolicy: AgentEnvironment, Environment: toolEnvironment(directory, definition)}
}

// runConnectQuietly runs one upstream connect action of the Tool without a terminal; its output goes to the diagnostic stream.
func runConnectQuietly(directory string, current toolSelection, definition catalog.Tool, action string, effects Effects, stderr io.Writer) error {
	args, err := definition.ResolveConnectAction(action)
	if err != nil {
		return err
	}
	process := toolProcess(directory, current, definition, args)
	if _, err := os.Stat(process.Program); err != nil {
		return fmt.Errorf("the launcher is missing: %w", err)
	}
	process.Stdout, process.Stderr = stderr, stderr
	return effects.Start(process)
}

// managedToolCommandLink returns the target of the Tool's command link when it points into the Tool
// directory, an empty target when the link is absent, and an error for a link the Tool does not own.
func managedToolCommandLink(command, directory string) (string, error) {
	target, err := os.Readlink(command)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("refusing unmanaged command %s: %w; remove it by hand", command, err)
	}
	if !strings.HasPrefix(target, directory+string(filepath.Separator)) {
		return "", fmt.Errorf("refusing unmanaged command %s, which points outside the Tool directory; remove it by hand", command)
	}
	return target, nil
}

// inventoryUserProjects walks the Tool directory for Git repositories and linked worktrees and resolves
// each one's kind, repository, container, and target below the user files root (ADR-0009, D4). The walk
// descends into a found project, so a worktree nested in a repository's working tree is found too; it
// does not descend into a `.git` entry. A `.git` entry in a Tool-owned subtree, at the top of the Tool's own
// home, or outside it is a false positive that refuses the delete.
func inventoryUserProjects(home, directory string, effects Effects) ([]userProject, error) {
	var projects []userProject
	ownHome := toolOwnHome(directory)
	var walk func(path string) error
	walk = func(path string) error {
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() != ".git" {
				continue
			}
			relative, err := filepath.Rel(ownHome, path)
			if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || isToolOwned(relative) {
				return fmt.Errorf("a Git repository or worktree at %s lies among the Tool's own files, where no user project belongs; move it out by hand", path)
			}
			project := userProject{path: path, linked: !entry.IsDir(), target: filepath.Join(home, userFilesRoot, relative)}
			if project.linked {
				common, err := gitOutput(effects, path, "rev-parse", "--git-common-dir")
				if err != nil {
					return fmt.Errorf("%s: cannot find the repository of the worktree: %w", path, err)
				}
				if !filepath.IsAbs(common) {
					common = filepath.Join(path, common)
				}
				project.repository = filepath.Dir(filepath.Clean(common))
			}
			projects = append(projects, project)
		}
		for _, entry := range entries {
			if entry.IsDir() && entry.Name() != ".git" {
				if err := walk(filepath.Join(path, entry.Name())); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(directory); err != nil {
		return nil, err
	}
	for i := range projects {
		projects[i].container = containerOf(projects, projects[i].path)
		if projects[i].container != "" {
			continue
		}
		if _, err := os.Lstat(projects[i].target); err == nil {
			return nil, fmt.Errorf("%s would move to %s, which exists; move it aside first", projects[i].path, projects[i].target)
		}
	}
	return projects, nil
}

// containerOf returns the path of the nearest project whose directory holds the path, or empty when none does.
func containerOf(projects []userProject, path string) string {
	container := ""
	for _, other := range projects {
		if other.path != path && strings.HasPrefix(path, other.path+string(filepath.Separator)) && len(other.path) > len(container) {
			container = other.path
		}
	}
	return container
}

// isToolOwned reports whether a path relative to the Tool's own home lies in a subtree that holds Tool files only.
func isToolOwned(relative string) bool {
	first := strings.Split(relative, string(filepath.Separator))[0]
	for _, subtree := range toolOwnedSubtrees {
		if first == subtree {
			return true
		}
	}
	return false
}

// relocateUserProjects moves every top-level project to its target with Git's own commands, carries the
// projects it contains along with it, repairs the links of every moved repository, and verifies each result.
// A contained project keeps its place inside its container, so it moves when the container does.
func relocateUserProjects(projects []userProject, stdout io.Writer, effects Effects) error {
	ordered := slices.Clone(projects)
	sort.SliceStable(ordered, func(i, j int) bool { return len(ordered[i].path) < len(ordered[j].path) })
	newPath := map[string]string{}
	for _, project := range ordered {
		if project.container == "" {
			newPath[project.path] = project.target
			continue
		}
		relative, err := filepath.Rel(project.container, project.path)
		if err != nil {
			return err
		}
		newPath[project.path] = filepath.Join(newPath[project.container], relative)
	}
	// A top-level project's parent is created just before it moves, and each move is reported as it succeeds.
	reportMoved := func(top userProject) {
		for _, project := range projects {
			if project.path == top.path || strings.HasPrefix(project.path, top.path+string(filepath.Separator)) {
				fmt.Fprintf(stdout, "moved the %s %s to %s\n", project.kind(), project.path, newPath[project.path])
			}
		}
	}
	// Top-level linked worktrees move first, while their repositories are still in place.
	for _, project := range projects {
		if project.container != "" || !project.linked {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(project.target), 0700); err != nil {
			return fmt.Errorf("cannot prepare %s: %w", project.target, err)
		}
		if _, err := gitOutput(effects, project.repository, "worktree", "move", project.path, project.target); err != nil {
			return fmt.Errorf("cannot move the worktree %s to %s: %w", project.path, project.target, err)
		}
		reportMoved(project)
	}
	for _, project := range projects {
		if project.container != "" || project.linked {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(project.target), 0700); err != nil {
			return fmt.Errorf("cannot prepare %s: %w", project.target, err)
		}
		if err := os.Rename(project.path, project.target); err != nil {
			return fmt.Errorf("cannot move the repository %s to %s: %w", project.path, project.target, err)
		}
		reportMoved(project)
	}
	// A linked worktree whose repository stays where it is is repaired from that repository with its new path.
	for _, project := range projects {
		if !project.linked {
			continue
		}
		if _, moved := newPath[project.repository]; moved {
			continue
		}
		if _, err := gitOutput(effects, project.repository, "worktree", "repair", newPath[project.path]); err != nil {
			return fmt.Errorf("cannot repair the worktree %s from its repository %s: %w", newPath[project.path], project.repository, err)
		}
	}
	// Each repository repairs the links of its linked worktrees, named by their new paths.
	for _, project := range projects {
		if project.linked {
			continue
		}
		args := []string{"worktree", "repair"}
		for _, other := range projects {
			if other.linked && other.repository == project.path {
				args = append(args, newPath[other.path])
			}
		}
		if _, err := gitOutput(effects, newPath[project.path], args...); err != nil {
			return fmt.Errorf("cannot repair the repository %s after its move: %w", newPath[project.path], err)
		}
	}
	for _, project := range projects {
		current := newPath[project.path]
		if _, err := gitOutput(effects, current, "rev-parse", "--git-dir"); err != nil {
			return fmt.Errorf("%s is not usable with Git after its move: %w", current, err)
		}
		if !project.linked {
			continue
		}
		repository := project.repository
		if moved, ok := newPath[repository]; ok {
			repository = moved
		}
		list, err := gitOutput(effects, repository, "worktree", "list", "--porcelain")
		if err != nil || !strings.Contains(list, "worktree "+current+"\n") {
			return fmt.Errorf("%s is not listed by its repository after its move: %v", current, err)
		}
	}
	return nil
}

// gitOutput runs Git as agent in a directory and returns its trimmed standard output, or an error that carries its diagnostic.
func gitOutput(effects Effects, directory string, args ...string) (string, error) {
	var output, diagnostic bytes.Buffer
	err := effects.Start(Process{Program: "git", Args: args, Directory: directory, Identity: agentIdentity, EnvironmentPolicy: GitConfigEnvironment, Stdout: &output, Stderr: &diagnostic})
	if err != nil {
		if summary := strings.TrimSpace(diagnostic.String()); summary != "" {
			return "", errors.New(strings.SplitN(summary, "\n", 2)[0])
		}
		return "", err
	}
	return strings.TrimSpace(output.String()), nil
}
