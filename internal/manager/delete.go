package manager

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
}

// kind names the project for messages.
func (p userProject) kind() string {
	if p.linked {
		return "worktree"
	}
	return "repository"
}

// deleteTool removes a Tool and everything it stored, after relocating the user project files found
// inside its directory with Git's own commands, and reports what it moved and what remains.
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
	if err := stopService(directory, r.name, effects); err != nil {
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
	fmt.Fprintf(stdout, "%s deleted: the service, the installation, the user data, the credentials, and the selection are removed\n", r.name)
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

// inventoryUserProjects walks the Tool directory for Git repositories and linked worktrees without
// descending into a found project, and resolves each one's kind, repository, and target below the user
// files root (ADR-0009, D4). A `.git` entry in a Tool-owned subtree, at the top of the Tool's own home,
// or outside it is a false positive that refuses the delete.
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
			if _, err := os.Lstat(project.target); err == nil {
				return fmt.Errorf("%s would move to %s, which exists; move it aside first", path, project.target)
			}
			projects = append(projects, project)
			return nil
		}
		for _, entry := range entries {
			if entry.IsDir() {
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
	// Linked worktrees move before the repositories they may belong to, so `git worktree move` finds its repository in place.
	sort.SliceStable(projects, func(i, j int) bool { return projects[i].linked && !projects[j].linked })
	return projects, nil
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

// relocateUserProjects moves every inventoried project to its target with Git's own commands, names
// each move as it succeeds, repairs the links of moved repositories, and verifies each result.
func relocateUserProjects(projects []userProject, stdout io.Writer, effects Effects) error {
	moved := map[string]string{}
	for _, project := range projects {
		if err := os.MkdirAll(filepath.Dir(project.target), 0700); err != nil {
			return fmt.Errorf("cannot prepare %s: %w", project.target, err)
		}
		if project.linked {
			if _, err := gitOutput(effects, project.repository, "worktree", "move", project.path, project.target); err != nil {
				return fmt.Errorf("cannot move the worktree %s to %s: %w", project.path, project.target, err)
			}
		} else if err := os.Rename(project.path, project.target); err != nil {
			return fmt.Errorf("cannot move the repository %s to %s: %w", project.path, project.target, err)
		}
		moved[project.path] = project.target
		fmt.Fprintf(stdout, "moved the %s %s to %s\n", project.kind(), project.path, project.target)
	}
	for _, project := range projects {
		if project.linked {
			continue
		}
		// Linked worktrees of a moved repository that moved themselves name their new paths for the repair.
		args := []string{"worktree", "repair"}
		for _, other := range projects {
			if other.linked && other.repository == project.path {
				args = append(args, moved[other.path])
			}
		}
		if _, err := gitOutput(effects, project.target, args...); err != nil {
			return fmt.Errorf("cannot repair the repository %s after its move: %w", project.target, err)
		}
	}
	for _, project := range projects {
		if _, err := gitOutput(effects, project.target, "rev-parse", "--git-dir"); err != nil {
			return fmt.Errorf("%s is not usable with Git after its move: %w", project.target, err)
		}
		repository := project.target
		if project.linked {
			if repository = moved[project.repository]; repository == "" {
				repository = project.repository
			}
		}
		list, err := gitOutput(effects, repository, "worktree", "list", "--porcelain")
		if err != nil || !strings.Contains(list, "worktree "+project.target+"\n") {
			return fmt.Errorf("%s is not listed by its repository after its move: %v", project.target, err)
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
