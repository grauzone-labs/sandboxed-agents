package manualcheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

var (
	runIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,60}$`)
	groupPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	namePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// KnownSandboxes are the sandbox names the checklist creates; `list` may show no other.
var KnownSandboxes = []string{"chk-persist", "chk-vs", "chk-base", "chk-native", "chk-int", "agent01"}

// Run is one run of the checklist on one preview commit: its private directory and the facts
// that preflight verified. Every item reads these facts from run.json, so no item runs against an
// executable that preflight did not verify.
type Run struct {
	SchemaVersion    int    `json:"schema_version"`
	ID               string `json:"run"`
	Commit           string `json:"commit"`
	Version          string `json:"version"`
	AssetSHA256      string `json:"asset_sha256"`
	Executable       string `json:"executable"`
	ExecutableSHA256 string `json:"executable_sha256"`
	Platform         string `json:"platform"`
	Group            string `json:"group"`
	PodmanConnection string `json:"podman_connection,omitempty"`
	Host             string `json:"host"`
	CreatedUTC       string `json:"created_utc"`

	// Root is the run directory; it is not part of run.json.
	Root string `json:"-"`
	// Stdout receives the progress lines of the runners.
	Stdout io.Writer `json:"-"`
}

// RunRoot resolves the run directory of a run ID below parent, or below the state directory of
// the user when parent is empty, and creates it on first use.
func RunRoot(parent, id string) (string, error) {
	if !runIDPattern.MatchString(id) {
		return "", fmt.Errorf("run ID %q must match %s", id, runIDPattern)
	}
	if parent == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		parent = filepath.Join(stateDirectoryFor(runtime.GOOS, os.Getenv, home), "sandboxed-agents-manual")
	}
	root := filepath.Join(parent, id)
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	return root, nil
}

// stateDirectoryFor returns the per-user state directory of a platform: `%LOCALAPPDATA%` on
// Windows (or `AppData\Local` below the home directory when the variable is unset), and
// `$XDG_STATE_HOME` or `~/.local/state` elsewhere.
func stateDirectoryFor(goos string, getenv func(string) string, home string) string {
	if goos == "windows" {
		if local := getenv("LOCALAPPDATA"); local != "" {
			return local
		}
		return filepath.Join(home, "AppData", "Local")
	}
	if state := getenv("XDG_STATE_HOME"); state != "" {
		return state
	}
	return filepath.Join(home, ".local", "state")
}

// LoadRun reads run.json of a run directory and refuses a run that preflight has not verified,
// including one whose executable no longer has the SHA-256 the preflight recorded.
func LoadRun(root string) (*Run, error) {
	data, err := os.ReadFile(filepath.Join(root, "run.json"))
	if err != nil {
		return nil, fmt.Errorf("no verified run at %s; run preflight first (%w)", root, err)
	}
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, err
	}
	if run.SchemaVersion != 1 || !commitPattern.MatchString(run.Commit) || run.Version == "" || !sha256Pattern.MatchString(run.AssetSHA256) {
		return nil, errors.New("run.json does not describe a verified run")
	}
	if !groupPattern.MatchString(run.Group) || run.Group == "default" {
		return nil, errors.New("run.json names an invalid controller group")
	}
	if run.Platform != PlatformLinux && run.Platform != PlatformWindows {
		return nil, errors.New("run.json names an unknown platform")
	}
	if _, err := os.Stat(run.Executable); err != nil {
		return nil, fmt.Errorf("verified executable missing: %w", err)
	}
	sha, err := FileSHA256(run.Executable)
	if err != nil {
		return nil, fmt.Errorf("verified executable unreadable: %w", err)
	}
	if !sha256Pattern.MatchString(run.ExecutableSHA256) || !strings.EqualFold(sha, run.ExecutableSHA256) {
		return nil, fmt.Errorf("the executable %s changed since the preflight verified it; run a new preflight", run.Executable)
	}
	run.Root = root
	run.Stdout = os.Stdout
	return &run, nil
}

// Save writes run.json once; a run is never re-verified in place.
func (r *Run) Save() error {
	path := filepath.Join(r.Root, "run.json")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s exists; a fresh retest uses a new run ID", path)
	}
	return writePrivateJSON(path, r)
}

// ItemDir creates and returns the private directory of one item of the run.
func (r *Run) ItemDir(item string) (string, error) {
	dir := filepath.Join(r.Root, item)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

// Printf writes a progress line of a runner.
func (r *Run) Printf(format string, args ...any) {
	if r.Stdout != nil {
		fmt.Fprintf(r.Stdout, format, args...)
	}
}

// StepResult is the durable outcome of one recorded command: its process ID, times, exit status,
// and output file.
type StepResult struct {
	Name        string `json:"name"`
	Command     string `json:"command"`
	PID         int    `json:"pid"`
	StartedUTC  string `json:"started_utc"`
	FinishedUTC string `json:"finished_utc"`
	Exit        int    `json:"exit"`
	Log         string `json:"log"`
}

// Output returns the recorded output of the step.
func (s StepResult) Output() string {
	data, _ := os.ReadFile(s.Log)
	return string(data)
}

// Contains reports whether the recorded output contains text.
func (s StepResult) Contains(text string) bool {
	return strings.Contains(s.Output(), text)
}

// HasLine reports whether the recorded output has a line equal to text.
func (s StepResult) HasLine(text string) bool {
	for _, line := range strings.Split(strings.ReplaceAll(s.Output(), "\r\n", "\n"), "\n") {
		if line == text {
			return true
		}
	}
	return false
}

// Step runs one command with standard input from stdin (nil for none), records its output, PID,
// UTC start and end, and exit status under dir, and returns the result. A non-zero exit is a
// result, not an error; an error means the command could not be run.
func Step(dir, name string, stdin io.Reader, command *exec.Cmd) (StepResult, error) {
	log := filepath.Join(dir, name+".log")
	file, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return StepResult{}, err
	}
	defer file.Close()
	command.Stdout, command.Stderr = file, file
	if stdin != nil {
		command.Stdin = stdin
	}
	result := StepResult{Name: name, Command: strings.Join(command.Args, " "), StartedUTC: utcNow(), Log: log}
	if err := command.Start(); err != nil {
		result.FinishedUTC, result.Exit = utcNow(), -1
		fmt.Fprintf(file, "could not start: %v\n", err)
		_ = writePrivateJSON(filepath.Join(dir, name+".json"), result)
		return result, err
	}
	result.PID = command.Process.Pid
	err = command.Wait()
	result.FinishedUTC = utcNow()
	var exit *exec.ExitError
	switch {
	case err == nil:
		result.Exit = 0
	case errors.As(err, &exit):
		result.Exit = exit.ExitCode()
		if result.Exit < 0 {
			result.Exit = 128 + 9
		}
	default:
		result.Exit = -1
		fmt.Fprintf(file, "wait failed: %v\n", err)
	}
	if err := writePrivateJSON(filepath.Join(dir, name+".json"), result); err != nil {
		return result, err
	}
	return result, nil
}

// Verdict appends what one runner stage observed for an item to the item's verdict.txt and prints
// it. The file holds one block per stage in the order the stages ran, and its first line is the
// aggregate: `aggregate=deviation` once any stage of this run deviated, which no later stage
// clears, else `aggregate=ok`. A stage that is run again adds a block. A verdict.txt in another
// form is refused, so a run started by an earlier runner is continued only in a new run. Line breaks
// in the stage or the observation, such as those of an indented JSON failure, become single spaces,
// so every field stays on one line. The verdict describes observations; it is never an entry of the
// record.
func (r *Run) Verdict(dir, item string, deviated bool, observed string) error {
	item, observed = lineBreaks.ReplaceAllString(item, " "), lineBreaks.ReplaceAllString(observed, " ")
	r.Printf("observed: %s: %s\n", item, observed)
	path := filepath.Join(dir, "verdict.txt")
	aggregate, blocks := "ok", ""
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		header, rest, _ := strings.Cut(string(data), "\n")
		if header != "aggregate=ok" && header != "aggregate=deviation" {
			return fmt.Errorf("%s predates the per-stage verdict; continue the item in a new run", path)
		}
		if header == "aggregate=deviation" || strings.Contains(rest, "\nresult=deviation\n") {
			aggregate = "deviation"
		}
		blocks = rest
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	result := "ok"
	if deviated {
		result, aggregate = "deviation", "deviation"
	}
	blocks += fmt.Sprintf("\nutc=%s\nitem=%s\nresult=%s\nobserved=%s\n", utcNow(), item, result, observed)
	return writePrivate(path, "aggregate="+aggregate+"\n"+blocks)
}

// lineBreaks matches a run of line breaks with the blanks around them, which Verdict folds into one
// space.
var lineBreaks = regexp.MustCompile(`[ \t]*[\r\n]+[ \t]*`)

// Checkpoint writes the exact steps only a person can perform to human-steps.txt in the item
// directory and prints them. The runner stops at it; nothing it did counts as those steps.
func (r *Run) Checkpoint(dir string, steps ...string) error {
	text := "HUMAN:\n"
	for index, step := range steps {
		text += fmt.Sprintf("%d. %s\n", index+1, step)
	}
	r.Printf("%s", text)
	return writePrivate(filepath.Join(dir, "human-steps.txt"), text)
}

// Failures counts the deviations of an item and turns them into one verdict.
type Failures struct {
	run   *Run
	count int
}

// Addf records one deviation and prints it.
func (f *Failures) Addf(format string, args ...any) {
	f.count++
	f.run.Printf("deviation: "+format+"\n", args...)
}

// Count returns the number of deviations.
func (f *Failures) Count() int { return f.count }

// Finish writes the verdict: the observed text when nothing deviated, else the count and a
// failing error.
func (f *Failures) Finish(dir, item, observed string) error {
	if f.count == 0 {
		return f.run.Verdict(dir, item, false, observed)
	}
	if err := f.run.Verdict(dir, item, true, fmt.Sprintf("%d deviation(s); the item fails", f.count)); err != nil {
		return err
	}
	return fmt.Errorf("%s: %d deviation(s); see %s", item, f.count, dir)
}

// utcNow returns the current UTC time in ISO 8601 form with seconds.
func utcNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

// writePrivate writes text to a file readable by the operator only.
func writePrivate(path, text string) error {
	return os.WriteFile(path, []byte(text), 0600)
}

// writePrivateJSON writes a value as indented JSON readable by the operator only.
func writePrivateJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}

// readJSON reads a JSON file into value.
func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}
