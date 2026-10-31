//go:build linux

// Package ownershipbench measures one supplied image in a fresh, private rootless Podman store: the
// time of the import and of a keep-id create, with samples of the engine's processes and free space,
// while never touching the active store. The measurement is a diagnostic of the ownership-mapping
// path; docs/ownership-benchmark.md states its procedure, limits, and the handling of its results.
package ownershipbench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Label marks the container and the workspace volume the benchmark creates, with the run's nonce as
// value, so that cleanup removes only objects this run proved to own.
const Label = "io.sandboxed-agents.benchmark"

// sampleInterval is the time between two samples of the engine's processes and the free space.
const sampleInterval = 200 * time.Millisecond

var (
	fullImageID     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	fullContainerID = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Options are the validated inputs of one measurement.
type Options struct {
	Archive      string
	Image        string
	BaselineInfo string
	Parent       string
	Driver       string
	MountProgram string
	PeakBytes    int64
	Timeout      time.Duration
}

// ParseOptions reads the command line and validates every input before anything is created: a
// non-root Linux user, a local archive, an existing parent directory outside the active graph and
// run roots of the rootless baseline, a full image ID, a positive peak bound that the free space
// exceeds, a timeout between 0.1 and 7200 seconds, and, with overlay only, an absolute mount helper.
// It returns the options and the baseline store information to record.
func ParseOptions(args []string, stderr io.Writer) (Options, map[string]any, error) {
	flags := flag.NewFlagSet("benchmark-ownership", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options Options
	var timeout float64
	flags.StringVar(&options.Archive, "archive", "", "saved image archive to load into the private store")
	flags.StringVar(&options.Image, "image", "", "full sha256 image ID from the supplied saved image")
	flags.StringVar(&options.BaselineInfo, "baseline-info", "", "saved podman info JSON from the source store")
	flags.StringVar(&options.Parent, "parent", "", "existing directory on the filesystem to compare")
	flags.StringVar(&options.Driver, "driver", "", "storage driver: overlay or vfs")
	flags.StringVar(&options.MountProgram, "mount-program", "", "explicit overlay helper; inspect actual store options in report.json")
	flags.Int64Var(&options.PeakBytes, "peak-bytes", 0, "conservative additional-space bound, including load temp and mapped copy")
	flags.Float64Var(&timeout, "timeout", 900, "seconds per load/create, at most 7200")
	if err := flags.Parse(args); err != nil {
		return options, nil, err
	}
	options.Timeout = time.Duration(timeout * float64(time.Second))
	if flags.NArg() != 0 {
		return options, nil, errors.New("unexpected arguments")
	}
	if os.Geteuid() == 0 {
		return options, nil, errors.New("requires a non-root Linux user")
	}
	if options.Archive == "" || options.Image == "" || options.BaselineInfo == "" || options.Parent == "" || options.PeakBytes == 0 {
		return options, nil, errors.New("--archive, --image, --baseline-info, --parent, --driver, and --peak-bytes are required")
	}
	if options.Driver != "overlay" && options.Driver != "vfs" {
		return options, nil, errors.New("--driver must be overlay or vfs")
	}
	var err error
	if options.Archive, err = resolveExisting(options.Archive); err != nil {
		return options, nil, err
	}
	if options.Parent, err = resolveExisting(options.Parent); err != nil {
		return options, nil, err
	}
	archive, archiveErr := os.Stat(options.Archive)
	parent, parentErr := os.Stat(options.Parent)
	if archiveErr != nil || !archive.Mode().IsRegular() || parentErr != nil || !parent.IsDir() || !fullImageID.MatchString(options.Image) {
		return options, nil, errors.New("require a local archive, existing parent directory and full sha256 image ID")
	}
	if options.PeakBytes <= 0 || timeout < 0.1 || timeout > 7200 {
		return options, nil, errors.New("require positive peak-bytes and timeout between 0.1 and 7200 seconds")
	}
	if options.MountProgram != "" {
		helper, err := os.Stat(options.MountProgram)
		if options.Driver != "overlay" || !filepath.IsAbs(options.MountProgram) || err != nil || !helper.Mode().IsRegular() {
			return options, nil, errors.New("mount-program requires overlay and an existing absolute helper path")
		}
	}
	baseline, err := readBaseline(options.BaselineInfo, options.Parent)
	if err != nil {
		return options, nil, err
	}
	if free, err := freeBytes(options.Parent); err != nil {
		return options, nil, err
	} else if free <= options.PeakBytes {
		return options, nil, errors.New("free space does not exceed the supplied peak bound")
	}
	return options, baseline, nil
}

// resolveExisting returns the absolute path with every link resolved, failing when it does not exist.
func resolveExisting(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

// readBaseline reads the saved podman info of the source store, requires it to describe rootless
// Podman, refuses a parent below its graph or run root, and returns the store fields to record.
func readBaseline(path, parent string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var source struct {
		Host  struct{ Security struct{ Rootless bool } }
		Store map[string]any
	}
	if err := json.Unmarshal(data, &source); err != nil {
		return nil, fmt.Errorf("baseline info: %w", err)
	}
	if !source.Host.Security.Rootless {
		return nil, errors.New("baseline must describe rootless Podman")
	}
	baseline := map[string]any{}
	for _, key := range []string{"graphRoot", "runRoot", "graphDriverName", "graphOptions", "graphStatus"} {
		baseline[key] = source.Store[key]
	}
	for _, key := range []string{"graphRoot", "runRoot"} {
		root, _ := baseline[key].(string)
		if root == "" {
			return nil, errors.New("benchmark parent must be outside the active graphroot/runroot")
		}
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
		if relative, err := filepath.Rel(root, parent); err == nil && relative != ".." && !strings.HasPrefix(relative, "../") {
			return nil, errors.New("benchmark parent must be outside the active graphroot/runroot")
		}
	}
	return baseline, nil
}

// freeBytes returns the bytes available to this user on the filesystem holding path.
func freeBytes(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Frsize), nil
}

// Measurement runs a fixed measurement sequence with bounded commands in one private, new store.
type Measurement struct {
	options     Options
	directory   string
	environment []string
	command     []string
	report      map[string]any
	elapsed     map[string]float64
}

// New creates the private store directory below the parent with mode 0700, writes the private
// storage configuration, and prepares the environment and the Podman command prefix of every
// command: the local engine with explicit root, run root, driver, volume path, and temporary
// directory, with the caller's container configuration overrides removed. It prints the directory.
func New(options Options, baseline map[string]any, stdout io.Writer) (*Measurement, error) {
	directory, err := os.MkdirTemp(options.Parent, "ownership-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return nil, err
	}
	free, err := freeBytes(options.Parent)
	if err != nil {
		return nil, err
	}
	m := &Measurement{options: options, directory: directory, elapsed: map[string]float64{}}
	m.report = map[string]any{"result": "incomplete", "baseline": baseline, "kernel": kernelRelease(), "image": options.Image, "peak_bytes": options.PeakBytes,
		"elapsed_seconds": m.elapsed, "cleanup": "not started", "free_before": free}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "CONTAINER_HOST", "CONTAINER_CONNECTION", "CONTAINER_SSHKEY", "STORAGE_OPTS", "CONTAINERS_CONF", "CONTAINERS_CONF_OVERRIDE", "CONTAINERS_STORAGE_CONF", "TMPDIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR":
			continue
		}
		m.environment = append(m.environment, entry)
	}
	for _, name := range []string{"graph", "run", "tmp", "config", "data", "runtime"} {
		if err := os.Mkdir(filepath.Join(directory, name), 0700); err != nil {
			return nil, err
		}
	}
	m.environment = append(m.environment, "TMPDIR="+filepath.Join(directory, "tmp"), "XDG_CONFIG_HOME="+filepath.Join(directory, "config"),
		"XDG_DATA_HOME="+filepath.Join(directory, "data"), "XDG_RUNTIME_DIR="+filepath.Join(directory, "runtime"))
	storage := filepath.Join(directory, "config", "storage.conf")
	configuration := fmt.Sprintf("[storage]\ndriver = %s\ngraphroot = %s\nrunroot = %s\n", jsonString(options.Driver), jsonString(filepath.Join(directory, "graph")), jsonString(filepath.Join(directory, "run")))
	if err := os.WriteFile(storage, []byte(configuration), 0600); err != nil {
		return nil, err
	}
	m.environment = append(m.environment, "CONTAINERS_STORAGE_CONF="+storage)
	m.command = []string{"podman", "--remote=false", "--root", filepath.Join(directory, "graph"), "--runroot", filepath.Join(directory, "run"),
		"--storage-driver", options.Driver, "--volumepath", filepath.Join(directory, "volumes")}
	if options.MountProgram != "" {
		m.command = append(m.command, "--storage-opt", "overlay.mount_program="+options.MountProgram)
	}
	m.command = append(m.command, "--tmpdir", filepath.Join(directory, "tmp"))
	m.report["podman_prefix"] = m.command
	fmt.Fprintln(stdout, directory)
	return m, nil
}

// jsonString quotes a configuration value as a JSON string, which TOML accepts.
func jsonString(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

// kernelRelease returns the running kernel's release string.
func kernelRelease() string {
	var name syscall.Utsname
	if err := syscall.Uname(&name); err != nil {
		return ""
	}
	var release []byte
	for _, c := range name.Release {
		if c == 0 {
			break
		}
		release = append(release, byte(c))
	}
	return string(release)
}

// Directory returns the private store directory of the measurement.
func (m *Measurement) Directory() string { return m.directory }

// run times one Podman command separately, records its output in NAME.stdout and NAME.stderr,
// samples the command's own process group and the free space into NAME.samples.jsonl while it
// runs, and returns its standard output. A deadline, a crossed free-space floor, or a cancelled
// context kills the command's process group, marks the cleanup as pending, and retains all state.
func (m *Measurement) run(ctx context.Context, name string, arguments []string, timeout time.Duration) (string, error) {
	started := time.Now()
	stdoutPath := filepath.Join(m.directory, name+".stdout")
	stdout, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", err
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(filepath.Join(m.directory, name+".stderr"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", err
	}
	defer stderr.Close()
	command := exec.Command(m.command[0], append(append([]string{}, m.command[1:]...), arguments...)...)
	command.Env = m.environment
	command.Stdout, command.Stderr = stdout, stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return "", err
	}
	defer func() { m.elapsed[name] = float64(time.Since(started).Round(time.Millisecond)) / float64(time.Second) }()
	samples, err := os.OpenFile(filepath.Join(m.directory, name+".samples.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		m.interrupt(command)
		return "", err
	}
	defer samples.Close()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	floor := min(m.options.PeakBytes/10, 1<<30)
	for {
		select {
		case err := <-done:
			if err != nil {
				return "", fmt.Errorf("%s exited %d; see %s.stderr", name, command.ProcessState.ExitCode(), name)
			}
			data, err := os.ReadFile(stdoutPath)
			return string(data), err
		default:
		}
		free, err := freeBytes(m.directory)
		if err != nil {
			free = -1
		}
		sample, _ := json.Marshal(map[string]any{"elapsed": time.Since(started).Seconds(), "free_bytes": free, "processes": processSample(command.Process.Pid)})
		samples.Write(append(sample, '\n'))
		var failure error
		switch {
		case ctx.Err() != nil:
			failure = fmt.Errorf("%s interrupted by %v", name, context.Cause(ctx))
		case time.Since(started) > timeout:
			failure = fmt.Errorf("%s exceeded its command deadline", name)
		case free < floor:
			failure = fmt.Errorf("%s crossed the reserved free-space floor", name)
		}
		if failure != nil {
			m.report["cleanup"] = "pending: command interrupted; engine/mapping state must be reconciled before removal"
			m.interrupt(command)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				m.report["owned_process"] = "still unverified after kill; do not remove store"
			}
			return "", failure
		}
		select {
		case <-time.After(sampleInterval):
		case err := <-done:
			done <- err
		}
	}
}

// interrupt kills the whole process group the command leads.
func (m *Measurement) interrupt(command *exec.Cmd) {
	if command.Process != nil {
		syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
}

// processSample records the state, start ticks, wait channel, and I/O counters of every process in
// the command's process group, from /proc; a process that exits between the directory listing and
// its reads is skipped, and a missing fact is recorded as the error that hid it.
func processSample(group int) []map[string]any {
	sample := []map[string]any{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return sample
	}
	for _, entry := range entries {
		pid := 0
		if _, err := fmt.Sscanf(entry.Name(), "%d", &pid); err != nil || fmt.Sprint(pid) != entry.Name() {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		_, after, found := strings.Cut(string(stat), ") ")
		fields := strings.Fields(after)
		if !found || len(fields) < 20 || fields[2] != fmt.Sprint(group) {
			continue
		}
		item := map[string]any{"pid": pid, "state": fields[0], "start_ticks": fields[19]}
		for _, name := range []string{"wchan", "io"} {
			data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), name))
			if err != nil {
				item[name] = err.Error()
			} else {
				item[name] = strings.TrimSpace(string(data))
			}
		}
		sample = append(sample, item)
	}
	return sample
}

// Execute imports the archive once, verifies the private rootless store and the loaded image ID,
// measures a keep-id create with the identity and workspace proof, and then removes only the
// objects it proved to own: the stopped container by ID, the labelled workspace volume, and the
// supplied image without pruning its parents.
func (m *Measurement) Execute(ctx context.Context) error {
	version, err := m.run(ctx, "version", []string{"--version"}, 30*time.Second)
	if err != nil {
		return err
	}
	m.report["podman_version"] = strings.TrimSpace(version)
	infoText, err := m.run(ctx, "store-info", []string{"info", "--format", "json"}, 30*time.Second)
	if err != nil {
		return err
	}
	var info struct {
		Host  struct{ Security struct{ Rootless bool } }
		Store json.RawMessage
	}
	if err := json.Unmarshal([]byte(infoText), &info); err != nil {
		return fmt.Errorf("store info: %w", err)
	}
	var store struct{ GraphRoot, RunRoot string }
	if err := json.Unmarshal(info.Store, &store); err != nil {
		return fmt.Errorf("store info: %w", err)
	}
	graphRoot, _ := filepath.EvalSymlinks(store.GraphRoot)
	runRoot, _ := filepath.EvalSymlinks(store.RunRoot)
	if !info.Host.Security.Rootless || graphRoot != filepath.Join(m.directory, "graph") || runRoot != filepath.Join(m.directory, "run") {
		return errors.New("Podman did not select the private rootless store")
	}
	m.report["store"] = info.Store
	backing, err := exec.CommandContext(ctx, "findmnt", "--json", "--target", m.directory, "--output", "FSTYPE,SOURCE,OPTIONS").Output()
	if err != nil {
		return fmt.Errorf("findmnt: %w", err)
	}
	m.report["backing_filesystem"] = string(backing)
	before, err := m.run(ctx, "before-images", []string{"images", "--all", "--no-trunc", "--format", "json"}, 30*time.Second)
	if err != nil {
		return err
	}
	var images []json.RawMessage
	if err := json.Unmarshal([]byte(before), &images); err != nil {
		return fmt.Errorf("image list: %w", err)
	}
	if len(images) != 0 {
		return errors.New("new store unexpectedly contains images")
	}
	archiveHash, err := fileSHA256(m.options.Archive)
	if err != nil {
		return err
	}
	m.report["archive_sha256"] = archiveHash
	if _, err := m.run(ctx, "load", []string{"load", "--input", m.options.Archive}, m.options.Timeout); err != nil {
		return err
	}
	loaded, err := m.run(ctx, "loaded-image", []string{"image", "inspect", m.options.Image}, 30*time.Second)
	if err != nil {
		return err
	}
	var inspected []struct{ Id string }
	if err := json.Unmarshal([]byte(loaded), &inspected); err != nil || len(inspected) != 1 {
		return errors.New("image inspect did not describe one image")
	}
	if strings.TrimPrefix(inspected[0].Id, "sha256:") != strings.TrimPrefix(m.options.Image, "sha256:") {
		return errors.New("loaded image does not match the supplied full image ID")
	}
	m.report["loaded_image_id"] = inspected[0].Id
	nonce := filepath.Base(m.directory)
	volume := nonce + "-workspace"
	if _, err := m.run(ctx, "workspace", []string{"volume", "create", "--label", Label + "=" + nonce, volume}, 30*time.Second); err != nil {
		return err
	}
	check := `test "$(id -u):$(id -g)" = 0:0; chown 1000:1000 /workspace; ` +
		`runuser -u agent -- sh -ec 'test "$(id -u):$(id -g)" = 1000:1000; ` +
		`touch /workspace/probe; test "$(stat -c %u:%g /workspace/probe)" = 1000:1000; ` +
		`echo identity/workspace:1000:1000'`
	created, err := m.run(ctx, "create", []string{"create", "--name", nonce, "--label", Label + "=" + nonce,
		"--pull=never", "--userns=keep-id:uid=1000,gid=1000", "--user=0:0", "--network=none",
		"--security-opt=no-new-privileges", "--pids-limit=256", "--memory=4g", "--memory-swap=4g",
		"--cpus=2", "--mount", "type=volume,source=" + volume + ",destination=/workspace",
		"--entrypoint=/bin/sh", m.options.Image, "-ec", check}, m.options.Timeout)
	if err != nil {
		return err
	}
	container := strings.TrimSpace(created)
	if !fullContainerID.MatchString(container) {
		return errors.New("create did not return a full container ID")
	}
	m.report["container_id"] = container
	output, err := m.run(ctx, "identity", []string{"start", "--attach", container}, min(m.options.Timeout, 60*time.Second))
	if err != nil {
		return err
	}
	if strings.TrimSpace(output) != "identity/workspace:1000:1000" {
		return errors.New("identity/workspace proof missing")
	}
	ownedText, err := m.run(ctx, "owned-container", []string{"container", "inspect", container}, 30*time.Second)
	if err != nil {
		return err
	}
	var owned []struct {
		Id     string
		Config struct{ Labels map[string]string }
		State  struct{ Running bool }
	}
	if err := json.Unmarshal([]byte(ownedText), &owned); err != nil || len(owned) != 1 {
		return errors.New("container inspect did not describe one container")
	}
	if owned[0].Config.Labels[Label] != nonce || owned[0].State.Running || owned[0].Id != container {
		return errors.New("container ownership or stopped state is unverified; retaining it")
	}
	if _, err := m.run(ctx, "remove-container", []string{"rm", container}, 30*time.Second); err != nil {
		return err
	}
	volumeText, err := m.run(ctx, "owned-workspace", []string{"volume", "inspect", volume}, 30*time.Second)
	if err != nil {
		return err
	}
	var volumes []struct{ Labels map[string]string }
	if err := json.Unmarshal([]byte(volumeText), &volumes); err != nil || len(volumes) != 1 {
		return errors.New("volume inspect did not describe one volume")
	}
	if volumes[0].Labels[Label] != nonce {
		return errors.New("workspace ownership is unverified; retaining it")
	}
	if _, err := m.run(ctx, "remove-workspace", []string{"volume", "rm", volume}, 30*time.Second); err != nil {
		return err
	}
	// The initially empty private store plus the successful load and ID check prove this one image.
	// Parents are not pruned and the store is not deleted: other imported IDs were not individually proven.
	if _, err := m.run(ctx, "remove-image", []string{"rmi", "--no-prune", m.options.Image}, 30*time.Second); err != nil {
		return err
	}
	remaining, err := m.run(ctx, "remaining-images", []string{"images", "--all", "--no-trunc", "--format", "json"}, 30*time.Second)
	if err != nil {
		return err
	}
	m.report["retained_images"] = json.RawMessage(remaining)
	m.report["cleanup"] = "owned container/workspace/image removed; unproven parents and store directory retained"
	m.report["result"] = "pass"
	return nil
}

// fileSHA256 returns the lowercase hex SHA-256 of a file.
func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Fail records a failed result with its error, and marks the cleanup as pending when no command
// changed it, so that a failure before or outside a command still retains the store for inspection.
func (m *Measurement) Fail(err error) {
	m.report["result"] = "fail"
	message := err.Error()
	if message == "" {
		message = "interrupted"
	}
	m.report["error"] = message
	if m.report["cleanup"] == "not started" {
		m.report["cleanup"] = "pending: failure retained store; inspect recorded objects before removal"
	}
}

// WriteReport records the free space after the run and writes report.json into the store directory.
func (m *Measurement) WriteReport() error {
	free, err := freeBytes(m.options.Parent)
	if err != nil {
		free = -1
	}
	m.report["free_after"] = free
	data, err := json.MarshalIndent(m.report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.directory, "report.json"), append(data, '\n'), 0600)
}

// Passed tells whether the measurement completed with a pass result.
func (m *Measurement) Passed() bool { return m.report["result"] == "pass" }

// Main runs the benchmark command with the given arguments and returns its exit status: zero only
// for a passed measurement. SIGTERM and SIGINT end the running command through the same owned
// process-group kill as a deadline and retain the store.
func Main(args []string, stdout, stderr io.Writer) int {
	options, baseline, err := ParseOptions(args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "benchmark-ownership:", err)
		return 2
	}
	measurement, err := New(options, baseline, stdout)
	if err != nil {
		fmt.Fprintln(stderr, "benchmark-ownership:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := measurement.Execute(ctx); err != nil {
		measurement.Fail(err)
		fmt.Fprintln(stderr, "benchmark-ownership:", err)
	}
	if err := measurement.WriteReport(); err != nil {
		fmt.Fprintln(stderr, "benchmark-ownership: report:", err)
		return 1
	}
	if measurement.Passed() {
		return 0
	}
	return 1
}
