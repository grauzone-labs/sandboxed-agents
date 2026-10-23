package manualcheck_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manualcheck"
)

// TestStepRecordsDurableResult proves Step records the command line, PID, UTC times, exit status,
// and output of a command as private files and reports a non-zero exit as a result, through the
// real process fake.
func TestStepRecordsDurableResult(t *testing.T) {
	f := fake(t, "tool")
	f.script(t, "tool", map[string]response{"hello": {Output: "hi\n", Exit: 3}})
	dir := t.TempDir()
	result, err := manualcheck.Step(dir, "greet", nil, exec.Command(f.path("tool"), "hello"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Exit != 3 || result.PID == 0 || result.StartedUTC == "" || result.FinishedUTC == "" || !result.HasLine("hi") {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, "greet.json")), `"exit": 3`) {
		t.Fatal("greet.json lacks the exit status")
	}
	if runtimeIsUnix() {
		info, _ := os.Stat(filepath.Join(dir, "greet.log"))
		if info.Mode().Perm() != 0600 {
			t.Fatalf("log mode %v", info.Mode().Perm())
		}
	}
}

// TestLoadRunRefusesUnverifiedRun proves no item runs without run.json from the preflight, and
// that a run.json with the default group, or an executable whose bytes changed since the preflight
// recorded its SHA-256, is rejected, through LoadRun.
func TestLoadRunRefusesUnverifiedRun(t *testing.T) {
	root := t.TempDir()
	if _, err := manualcheck.LoadRun(root); err == nil {
		t.Fatal("a run without run.json loaded")
	}
	f := fake(t, "sandboxed-agents")
	run := verifiedRun(t, f, f.path("sandboxed-agents"))
	if run.Group != "manual-check" {
		t.Fatalf("group %q", run.Group)
	}
	run.Group = "default"
	data, _ := os.ReadFile(filepath.Join(run.Root, "run.json"))
	if err := os.WriteFile(filepath.Join(run.Root, "run.json"), []byte(strings.Replace(string(data), `"group": "manual-check"`, `"group": "default"`, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := manualcheck.LoadRun(run.Root); err == nil {
		t.Fatal("the default group loaded")
	}
	if err := os.WriteFile(filepath.Join(run.Root, "run.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := manualcheck.LoadRun(run.Root); err != nil {
		t.Fatalf("the restored run did not load: %v", err)
	}
	executable, err := os.ReadFile(f.path("sandboxed-agents"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path("sandboxed-agents"), append(executable, '\n'), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := manualcheck.LoadRun(run.Root); err == nil || !strings.Contains(err.Error(), "changed since the preflight") {
		t.Fatalf("a tampered executable loaded: %v", err)
	}
}

// TestCaptureStateDetectsChange proves CaptureState writes list, containers, volumes, and both
// inspect outputs, and CompareFiles names the first differing line, through fake podman and
// sandboxed-agents outputs.
func TestCaptureStateDetectsChange(t *testing.T) {
	f := fake(t, "sandboxed-agents", "podman")
	run := verifiedRun(t, f, f.path("sandboxed-agents"))
	f.script(t, "sandboxed-agents", map[string]response{"list": {Output: "NAME STATE\nchk-int running\n"}})
	f.script(t, "podman", map[string]response{
		"ps --all --format *":                                  {Output: "b 2 img running\na 1 img running\n"},
		"volume ls --format *":                                 {Output: "v2\nv1\n"},
		"inspect sandboxed-agents.manual-check.chk-int":        {Output: "[inspect]\n"},
		"inspect sandboxed-agents-backup.manual-check.chk-int": {Output: "Error: no such object\n", Exit: 125},
	})
	before := filepath.Join(run.Root, "before.txt")
	if err := run.CaptureState(before, "chk-int"); err != nil {
		t.Fatal(err)
	}
	text := readFile(t, before)
	if text != "NAME STATE\nchk-int running\na 1 img running\nb 2 img running\nv1\nv2\n[inspect]\nError: no such object\n" {
		t.Fatalf("captured state:\n%s", text)
	}
	f.script(t, "sandboxed-agents", map[string]response{"list": {Output: "NAME STATE\nchk-int stopped\n"}})
	after := filepath.Join(run.Root, "after.txt")
	if err := run.CaptureState(after, "chk-int"); err != nil {
		t.Fatal(err)
	}
	err := manualcheck.CompareFiles(before, after)
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("compare: %v", err)
	}
	if err := manualcheck.CompareFiles(before, before); err != nil {
		t.Fatal(err)
	}
}

// TestImagesInventoryAndCompare proves the inventory lists full image IDs with their references and
// the containers' image IDs, and the comparison names every ID that disappeared, through fake
// podman output.
func TestImagesInventoryAndCompare(t *testing.T) {
	f := fake(t, "sandboxed-agents", "podman")
	run := verifiedRun(t, f, f.path("sandboxed-agents"))
	idA, idB := strings.Repeat("1", 64), strings.Repeat("2", 64)
	f.script(t, "podman", map[string]response{
		"images --all --no-trunc --format *":       {Output: "sha256:" + idB + " localhost/x:none\nsha256:" + idA + " <none>:<none>\n"},
		"ps --all --format {{.Names}}":             {Output: "c1\n"},
		"container inspect --format {{.Image}} c1": {Output: idA + "\n"},
	})
	if _, err := run.ImagesInventory("images-before"); err != nil {
		t.Fatal(err)
	}
	text := readFile(t, filepath.Join(run.Root, "no-image-removal", "images-before.txt"))
	if !strings.HasPrefix(text, "# images: id repository:tag\n"+idA+" <none>:<none>\n"+idB+" localhost/x:none\n# containers: name image-id\nc1 "+idA+"\n") {
		t.Fatalf("inventory:\n%s", text)
	}
	f.script(t, "podman", map[string]response{"*": {}, "images --all --no-trunc --format *": {Output: "sha256:" + idB + " localhost/x:none\n"}})
	if _, err := run.ImagesInventory("images-after"); err != nil {
		t.Fatal(err)
	}
	err := run.ImagesCompare("images-before", "images-after")
	if err == nil || !strings.Contains(readFile(t, filepath.Join(run.Root, "no-image-removal", "verdict.txt")), idA) {
		t.Fatalf("a removed image was not reported: %v", err)
	}
	if err := run.ImagesCompare("images-after", "images-after"); err != nil {
		t.Fatal(err)
	}
}

// TestLoginNeedsTerminal proves the item observes three non-zero refusals with the documented
// message and unchanged state and sign-ins, and fails when one login exits zero, through the fake
// executable with standard input from the null device.
func TestLoginNeedsTerminal(t *testing.T) {
	f := fake(t, "sandboxed-agents", "podman")
	run := verifiedRun(t, f, f.path("sandboxed-agents"))
	refused := map[string]response{
		"list":                                  {Output: "NAME STATE\nchk-persist running\n"},
		"shell chk-persist":                     {Output: "Logged in to github.com\ngh exit 0\n{account}\naz exit 0\n"},
		"integrations login chk-persist github": {Output: "github login needs an interactive terminal\n", Exit: 1},
		"integrations login chk-persist azure device-code": {Output: "azure login needs an interactive terminal\n", Exit: 1},
		"integrations login chk-persist azure browser":     {Output: "azure login needs an interactive terminal\n", Exit: 1},
	}
	f.script(t, "sandboxed-agents", refused)
	if err := run.LoginNeedsTerminal("chk-persist"); err != nil {
		t.Fatal(err)
	}
	verdict := readFile(t, filepath.Join(run.Root, "login-needs-terminal", "verdict.txt"))
	if !strings.Contains(verdict, "three logins refused") {
		t.Fatalf("verdict:\n%s", verdict)
	}
	refused["integrations login chk-persist azure browser"] = response{Output: "signed in\n", Exit: 0}
	f.script(t, "sandboxed-agents", refused)
	if err := run.LoginNeedsTerminal("chk-persist"); err == nil {
		t.Fatal("a login that exited zero passed the item")
	}
}

// TestAzureToolchainRequired proves the item creates both sandboxes, observes six refusals naming
// the toolchain and the exact update command, compares the state, checks for no credential helper,
// and removes the sandboxes, and fails when the printed set contains none, through the fake
// executable.
func TestAzureToolchainRequired(t *testing.T) {
	f := fake(t, "sandboxed-agents", "podman")
	run := verifiedRun(t, f, f.path("sandboxed-agents"))
	responses := map[string]response{
		"*":                               {},
		"list":                            {Output: "NAME STATE\n"},
		"shell *":                         {Output: "git exit 1\n"},
		"integrations login chk-base *":   {Output: "azure login requires the azure toolchain; run update chk-base --with azure\n", Exit: 1},
		"integrations login chk-native *": {Output: "azure login requires the azure toolchain; run update chk-native --with azure,native\n", Exit: 1},
	}
	f.script(t, "sandboxed-agents", responses)
	if err := run.AzureToolchainRequired(false); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(f.calls(t, "sandboxed-agents"), "\n")
	for _, want := range []string{"up chk-base", "up chk-native --with native", "remove chk-base --volumes", "remove chk-native --volumes", "integrations login chk-native azdo --organization https://dev.azure.com/example-organization"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing call %q in:\n%s", want, calls)
		}
	}
	responses["integrations login chk-base *"] = response{Output: "azure login requires the azure toolchain; run update chk-base --with azure,none\n", Exit: 1}
	f.script(t, "sandboxed-agents", responses)
	if err := run.AzureToolchainRequired(true); err == nil {
		t.Fatal("a set containing none passed the item")
	}
}

// TestUpdateInterrupted proves the item kills exactly the update it started once the backup
// exists, observes check, list, all 25 refusals, the unchanged state, the recovery, the follow-up
// update, and the removal, through the fake executable and a rendezvous that keeps the update
// alive until the kill.
func TestUpdateInterrupted(t *testing.T) {
	image := "sha256:" + strings.Repeat("1", 64) + " localhost/sandboxed-agents:none-x\n"
	run, output, err := runUpdateInterrupted(t, image, image)
	if err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
	dir := filepath.Join(run.Root, "update-interrupted")
	interruption := readFile(t, filepath.Join(dir, "interruption.json"))
	if !strings.Contains(interruption, `"backup_seen": true`) || !strings.Contains(interruption, `"kill_signal": "KILL"`) {
		t.Fatalf("interruption:\n%s", interruption)
	}
	results := splitLines(readFile(t, filepath.Join(dir, "refusals-results.txt")))
	if len(results) != 25 {
		t.Fatalf("want 25 refusal results, got %d", len(results))
	}
	for _, line := range results {
		if !strings.HasSuffix(line, "names update chk-int: true") || strings.Contains(line, "| exit 0 |") {
			t.Fatalf("refusal result %q", line)
		}
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, "verdict.txt")), "25 refusals non-zero") {
		t.Fatal("verdict lacks the refusals")
	}
	// The unchanged inventory and volume list were compared, not merely written.
	for _, name := range []string{"images-before-recovery.txt", "images-after-recovery.txt"} {
		if !strings.Contains(readFile(t, filepath.Join(dir, name)), strings.Repeat("1", 64)) {
			t.Fatalf("%s lacks the image the fake podman listed", name)
		}
	}
	if volumes := readFile(t, filepath.Join(dir, "volumes-recovery.txt")); strings.Count(volumes, "sandboxed-agents.manual-check.chk-int.home") != 2 {
		t.Fatalf("volumes-recovery.txt:\n%s", volumes)
	}
}

// runUpdateInterrupted runs item update-interrupted against the fake executable and a fake podman
// whose `podman images` answers imagesBefore before the recovery and imagesAfter after it, and
// returns the run, the runner's progress output, and the item's error.
func runUpdateInterrupted(t *testing.T, imagesBefore, imagesAfter string) (*manualcheck.Run, string, error) {
	t.Helper()
	f := fake(t, "sandboxed-agents", "podman")
	run := verifiedRun(t, f, f.path("sandboxed-agents"))
	type answer = struct {
		Output string `json:",omitempty"`
		Exit   int    `json:",omitempty"`
	}
	f.script(t, "podman", map[string]response{
		"container exists sandboxed-agents-backup.manual-check.chk-int":     {Sequence: []answer{{Exit: 0}, {Exit: 1}}},
		"images --all --no-trunc --format {{.ID}} {{.Repository}}:{{.Tag}}": {Sequence: []answer{{Output: imagesBefore}, {Output: imagesAfter}}},
		"volume ls --format {{.Name}}":                                      {Output: "sandboxed-agents.manual-check.chk-int.home\n"},
		"*":                                                                 {},
	})
	released := filepath.Join(t.TempDir(), "released")
	f.script(t, "sandboxed-agents", map[string]response{
		"up chk-int":                   {Sequence: []answer{{}, {Output: `sandbox "chk-int" has an interrupted update; use update chk-int` + "\n", Exit: 1}}},
		"update chk-int --with native": {WaitFor: released, Sequence: []answer{{Output: "replacing\n"}, {Output: "sandbox chk-int already up to date\n"}}},
		"check chk-int":                {ReadyFile: released, Output: `sandbox "chk-int" has an interrupted update; use update chk-int` + "\n", Exit: 1},
		"list":                         {Output: "NAME STATE\nchk-int update interrupted\n"},
		"update chk-int":               {Output: "recovered\n"},
		"remove chk-int --volumes":     {},
		"*":                            {Output: `sandbox "chk-int" has an interrupted update; use update chk-int` + "\n", Exit: 1},
	})
	var output bytes.Buffer
	run.Stdout = &output
	err := run.UpdateInterrupted("chk-int", false)
	return run, output.String(), err
}

// TestUpdateInterruptedRecoveryChangesNoImage proves the recovery's image invariant in both
// directions: an image that appears across the recovery (built or pulled) and an image that
// disappears across it each fail the item with their own deviation, through a fake podman whose
// inventory answers differently before and after the recovery.
func TestUpdateInterruptedRecoveryChangesNoImage(t *testing.T) {
	kept := "sha256:" + strings.Repeat("1", 64) + " localhost/sandboxed-agents:none-x\n"
	built := "sha256:" + strings.Repeat("2", 64) + " localhost/sandboxed-agents:none-y\n"
	for name, c := range map[string]struct{ before, after, want string }{
		"an image appears":    {kept, kept + built, "the recovery added images (built or pulled): [" + strings.Repeat("2", 64) + "]"},
		"an image disappears": {kept + built, kept, "the recovery removed images: [" + strings.Repeat("2", 64) + "]"},
	} {
		t.Run(name, func(t *testing.T) {
			run, output, err := runUpdateInterrupted(t, c.before, c.after)
			if err == nil {
				t.Fatalf("the item passed although the inventory changed:\n%s", output)
			}
			if !strings.Contains(output, c.want) {
				t.Fatalf("missing deviation %q:\n%s", c.want, output)
			}
			if verdict := readFile(t, filepath.Join(run.Root, "update-interrupted", "verdict.txt")); !strings.Contains(verdict, "deviation") {
				t.Fatalf("verdict:\n%s", verdict)
			}
		})
	}
}

// TestUpdateInterruptedNotObserved proves an update that finishes before the backup appears is
// recorded as not observed and the item does not pass, through the fake executable.
func TestUpdateInterruptedNotObserved(t *testing.T) {
	f := fake(t, "sandboxed-agents", "podman")
	run := verifiedRun(t, f, f.path("sandboxed-agents"))
	t.Setenv(envPrefix("podman")+"_EXIT", "1")
	f.script(t, "sandboxed-agents", map[string]response{"*": {}})
	err := run.UpdateInterrupted("chk-int", true)
	if err == nil || !strings.Contains(readFile(t, filepath.Join(run.Root, "update-interrupted", "verdict.txt")), "not observed") {
		t.Fatalf("err = %v", err)
	}
}

// TestPersistenceObserve proves a round records the agent statuses, the pin, the clones, the
// hashed Azure account, the fresh commits' authors, and the stored credential, prints the session
// checkpoint, and fails when the pin or an author differs, through the fake executable.
func TestPersistenceObserve(t *testing.T) {
	f := fake(t, "sandboxed-agents", "podman")
	run := verifiedRun(t, f, f.path("sandboxed-agents"))
	dir := filepath.Join(run.Root, "persistence")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	config := `{"sandbox":"chk-persist","opencode_version":"1.19.3","github_repo":"https://github.com/o/r.git","azdo_organization":"https://dev.azure.com/o","azdo_repo":"https://dev.azure.com/o/p/_git/r","azure_account_sha256":"` + strings.Repeat("c", 64) + `","setup_utc":"x"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	inSandbox := "Logged in to github.com\ngh exit 0\ngh-clone exit 0\naz-account-sha256 " + strings.Repeat("c", 64) + "\nazdo-clone exit 0\nazdo-projects ID Name\nazdo-projects exit 0\nrepo-commit exit 0\nrepo-author Manual Check <manual-check@example.invalid> round-1\ngh-commit exit 0\ngh-author Manual Check <manual-check@example.invalid> round-1\ncredential-count 1\n"
	responses := map[string]response{
		"agents status chk-persist *":        {Output: "x: enabled; version 2; pin none; session false; signed in unknown\n"},
		"agents status chk-persist opencode": {Output: "opencode: enabled; version 1.19.3; pin 1.19.3; session false; signed in unknown\n"},
		"shell chk-persist":                  {Output: inSandbox},
	}
	for _, agent := range []string{"copilot", "claude", "codex"} {
		responses["agents status chk-persist "+agent] = response{Output: agent + ": enabled; version 2; pin none; session false; signed in unknown\n"}
	}
	f.script(t, "sandboxed-agents", responses)
	if err := run.PersistenceObserve(1); err != nil {
		t.Fatal(err)
	}
	round := filepath.Join(dir, "round-1")
	if script := readFile(t, filepath.Join(round, "in-sandbox.json")); !strings.Contains(script, "shell chk-persist") {
		t.Fatalf("round 1 did not run in the sandbox: %s", script)
	}
	responses["shell chk-persist"] = response{Output: strings.ReplaceAll(inSandbox, "round-1", "round-0")}
	f.script(t, "sandboxed-agents", responses)
	if err := run.PersistenceObserve(0); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(f.calls(t, "sandboxed-agents"), "\n")
	if strings.Contains(calls, "git clone") {
		t.Fatal("the clone commands must run inside the sandbox script, not as host calls")
	}
	if !strings.Contains(readFile(t, filepath.Join(round, "human-steps.txt")), "agents session chk-persist AGENT") {
		t.Fatal("the session checkpoint is missing")
	}
	if strings.Contains(calls, "agents session") {
		t.Fatal("the runner started an agent session")
	}
	responses["shell chk-persist"] = response{Output: strings.Replace(inSandbox, "gh-author Manual Check", "gh-author Someone Else", 1)}
	f.script(t, "sandboxed-agents", responses)
	if err := run.PersistenceObserve(2); err == nil {
		t.Fatal("a wrong author passed the round")
	}
}

// TestPreviewAttestation proves the item lists exactly five files, verifies each attestation,
// checks both executables against SHA256SUMS with the runner's own hashing, compares the tag's
// commit, and fails on mismatching checksums, an unexpected asset, a missing attestation, and an old
// gh, through fake gh and git and executables and SHA256SUMS written where the item downloads.
func TestPreviewAttestation(t *testing.T) {
	f := fake(t, "sandboxed-agents", "gh", "git")
	run := verifiedRun(t, f, f.path("sandboxed-agents"))
	version := run.Version
	// The fake gh downloads nothing, so the files the item checks are placed where it downloads.
	download := func(valid bool) {
		dir := filepath.Join(run.Root, "preview-attestation", "download")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		var sums string
		for name, content := range map[string]string{"sandboxed-agents-linux-amd64": "linux", "sandboxed-agents-windows-amd64.exe": "windows"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			sha, _ := manualcheck.FileSHA256(filepath.Join(dir, name))
			if !valid {
				sha = strings.Repeat("0", 64)
			}
			sums += sha + "  " + name + "\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums), 0600); err != nil {
			t.Fatal(err)
		}
	}
	assets := "sandboxed-agents-linux-amd64\nsandboxed-agents-windows-amd64.exe\nSHA256SUMS\nsandboxed-agents-" + version + ".tgz\nSandboxedAgents." + version + ".nupkg\n"
	gh := map[string]response{
		"--version":                       {Output: "gh version 2.102.0 (2026-01-01)\n"},
		"release view v" + version + " *": {Output: assets},
		"release download *":              {},
		"attestation verify *":            {Output: "Loaded 1 attestation\n"},
	}
	f.script(t, "gh", gh)
	f.script(t, "git", map[string]response{"ls-remote *": {Output: run.Commit + "\trefs/tags/v" + version + "^{}\n"}})
	download(true)
	if err := run.PreviewAttestation("v"+version, "o/r"); err != nil {
		t.Fatal(err)
	}
	download(false)
	if err := run.PreviewAttestation("v"+version, "o/r"); err == nil {
		t.Fatal("the executables passed without matching SHA256SUMS entries")
	}
	if err := run.PreviewAttestation("v9.9.9", "o/r"); err == nil {
		t.Fatal("a tag of another version was accepted")
	}
	// Validation records uploaded to the prerelease are listed beside the five files and are not attested.
	gh["release view v"+version+" *"] = response{Output: assets + "live-suite-linux.json\nlive-suite-windows-11.json\n"}
	f.script(t, "gh", gh)
	download(true)
	if err := run.PreviewAttestation("v"+version, "o/r"); err != nil {
		t.Fatalf("validation records beside the release files failed the item: %v", err)
	}
	if calls := strings.Join(f.calls(t, "gh"), "\n"); strings.Contains(calls, "attestation verify "+filepath.Join("", "live-suite-linux.json")) || strings.Count(calls, "attestation verify") != 15 {
		t.Fatalf("attestations must be verified for the five release files only:\n%s", calls)
	}
	gh["release view v"+version+" *"] = response{Output: assets + "notes.txt\n"}
	f.script(t, "gh", gh)
	if err := run.PreviewAttestation("v"+version, "o/r"); err == nil {
		t.Fatal("an unexpected asset passed the item")
	}
	gh["release view v"+version+" *"] = response{Output: assets}
	f.script(t, "gh", gh)
	gh["attestation verify *"] = response{Output: "no attestations\n", Exit: 1}
	f.script(t, "gh", gh)
	if err := run.PreviewAttestation("v"+version, "o/r"); err == nil {
		t.Fatal("a failed attestation passed the item")
	}
	f.script(t, "gh", map[string]response{"--version": {Output: "gh version 2.49.0 (2024-01-01)\n"}})
	if err := run.PreviewAttestation("v"+version, "o/r"); err == nil || !strings.Contains(err.Error(), "2.67.0") {
		t.Fatalf("an old gh was accepted: %v", err)
	}
}

// TestFinalCleanup proves the cleanup removes the setup and the sandbox, observes the removal half
// of credentials-persist in a fresh sandbox, compares the images, checks that nothing of the group
// remains, and prints the revocation checkpoint, through the fake executable and podman.
func TestFinalCleanup(t *testing.T) {
	f := fake(t, "sandboxed-agents", "podman")
	run := verifiedRun(t, f, f.path("sandboxed-agents"))
	if _, err := run.ImagesInventory("images-before"); err != nil {
		t.Fatal(err)
	}
	f.script(t, "sandboxed-agents", map[string]response{
		"*":                                  {},
		"shell chk-persist":                  {Output: "credentials-file absent\nuser-email []\nYou are not logged into any GitHub hosts\ngh exit 1\nPlease run 'az login'\naz exit 1\n"},
		"agents status chk-persist opencode": {Output: "opencode: not enabled\n"},
		"list":                               {Output: "NAME STATE\n"},
	})
	if err := run.FinalCleanup("chk-persist"); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(f.calls(t, "sandboxed-agents"), "\n")
	for _, want := range []string{"ssh-config chk-persist --remove", "remove chk-persist --volumes", "up chk-persist --with azure"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if !strings.Contains(readFile(t, filepath.Join(run.Root, "final-cleanup", "human-steps.txt")), "Revoke") {
		t.Fatal("the revocation checkpoint is missing")
	}
}

// TestVSCodePrepareStopsAtTheEditor proves the preparation creates the sandbox with the SSH setup,
// proves the host entry and an SSH login as agent, and leaves the editor steps to the person,
// through the fake executable and ssh.
func TestVSCodePrepareStopsAtTheEditor(t *testing.T) {
	f := fake(t, "sandboxed-agents", "ssh")
	run := verifiedRun(t, f, f.path("sandboxed-agents"))
	f.script(t, "sandboxed-agents", map[string]response{"*": {}, "ssh-config chk-vs": {Output: "Host chk-vs.manual-check\n  HostName 127.0.0.1\n"}, "shell chk-vs": {Output: "-rw-r--r-- 1 agent agent 0 /workspace/note.txt\n"}})
	f.script(t, "ssh", map[string]response{"-o BatchMode=yes chk-vs.manual-check whoami": {Output: "agent\n"}})
	if err := run.VSCodePrepare("chk-vs"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, filepath.Join(run.Root, "vscode-remote-ssh", "human-steps.txt")), "Remote-SSH: Connect to Host") {
		t.Fatal("the editor checkpoint is missing")
	}
	if err := run.VSCodeVerify("chk-vs", "note.txt"); err != nil {
		t.Fatal(err)
	}
	if err := run.VSCodeVerify("chk-vs", "../etc"); err == nil {
		t.Fatal("a path was accepted as a file name")
	}
}

// TestVSCodeVerdictKeepsAFailedStage proves a later stage never hides an earlier deviation: a
// failed verification, its successful retry, and a successful cleanup leave verdict.txt with the
// aggregate `deviation` and one block per stage in order; a clean sequence in a fresh run ends with
// the aggregate `ok`; and a verdict.txt of the earlier single-block form is refused rather than
// extended, through the fake executable at the item's public stages.
func TestVSCodeVerdictKeepsAFailedStage(t *testing.T) {
	f := fake(t, "sandboxed-agents")
	run := verifiedRun(t, f, f.path("sandboxed-agents"))
	present := map[string]response{"*": {}, "shell chk-vs": {Output: "-rw-r--r-- 1 agent agent 0 /workspace/note.txt\n"}}
	f.script(t, "sandboxed-agents", map[string]response{"*": {}, "shell chk-vs": {Exit: 1}})
	if err := run.VSCodeVerify("chk-vs", "note.txt"); err == nil {
		t.Fatal("a missing file passed the verification")
	}
	f.script(t, "sandboxed-agents", present)
	if err := run.VSCodeVerify("chk-vs", "note.txt"); err != nil {
		t.Fatal(err)
	}
	if err := run.VSCodeCleanup("chk-vs"); err != nil {
		t.Fatal(err)
	}
	verdict := readFile(t, filepath.Join(run.Root, "vscode-remote-ssh", "verdict.txt"))
	failed := strings.Index(verdict, "item=vscode-remote-ssh (verification)\nresult=deviation\n")
	retried := strings.LastIndex(verdict, "item=vscode-remote-ssh (verification)\nresult=ok\n")
	cleaned := strings.Index(verdict, "item=vscode-remote-ssh (cleanup)\nresult=ok\n")
	if !strings.HasPrefix(verdict, "aggregate=deviation\n") || failed < 0 || retried < failed || cleaned < retried {
		t.Fatalf("a later stage hid the deviation:\n%s", verdict)
	}
	clean := verifiedRun(t, f, f.path("sandboxed-agents"))
	if err := clean.VSCodeVerify("chk-vs", "note.txt"); err != nil {
		t.Fatal(err)
	}
	if err := clean.VSCodeCleanup("chk-vs"); err != nil {
		t.Fatal(err)
	}
	if verdict := readFile(t, filepath.Join(clean.Root, "vscode-remote-ssh", "verdict.txt")); !strings.HasPrefix(verdict, "aggregate=ok\n") || strings.Count(verdict, "\nresult=ok\n") != 2 {
		t.Fatalf("clean sequence:\n%s", verdict)
	}
	legacy := verifiedRun(t, f, f.path("sandboxed-agents"))
	dir := filepath.Join(legacy.Root, "vscode-remote-ssh")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "verdict.txt"), []byte("utc=x\nitem=vscode-remote-ssh (verification)\nobserved=1 deviation(s); the item fails\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := legacy.VSCodeCleanup("chk-vs"); err == nil || !strings.Contains(err.Error(), "new run") {
		t.Fatalf("an earlier verdict format was extended: %v", err)
	}
}

// TestVerdictBlocksStayOneLinePerField proves a stage whose observation spans several lines, such
// as the indented JSON of a failed capture, still writes a block of exactly four one-line fields, and
// that a later clean stage keeps the sticky aggregate and the order, through `installers summarize`
// on a capture run with an indented capture.failed.json and then on a clean result.json.
func TestVerdictBlocksStayOneLinePerField(t *testing.T) {
	f := fake(t, "sandboxed-agents")
	run := verifiedRun(t, f, f.path("sandboxed-agents"))
	failed := filepath.Join(t.TempDir(), "npm-failed")
	if err := os.MkdirAll(failed, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(failed, "capture.failed.json"), []byte("{\r\n  \"failure\": \"budget\",\r\n  \"acceptance_changed\": false\r\n}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run.InstallersSummarize(failed); err == nil {
		t.Fatal("a failed capture summarized as clean")
	}
	clean := filepath.Join(t.TempDir(), "npm-clean")
	if err := os.MkdirAll(clean, 0700); err != nil {
		t.Fatal(err)
	}
	result := `{"schema_version":2,"method":"npm","trace_verdict":"NO_FINDINGS","environment_verdict":"NO_FINDINGS","kernel_complete":true,"captured_commit":"` + run.Commit + `"}`
	if err := os.WriteFile(filepath.Join(clean, "result.json"), []byte(result), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run.InstallersSummarize(clean); err != nil {
		t.Fatal(err)
	}
	verdict := readFile(t, filepath.Join(run.Root, "installers-no-podman", "verdict.txt"))
	lines := strings.Split(strings.TrimSuffix(verdict, "\n"), "\n")
	want := []string{"aggregate=deviation", "", "utc=", "item=installers-no-podman (npm-failed)", "result=deviation", "observed=capture.failed.json: {", "", "utc=", "item=installers-no-podman (npm-clean)", "result=ok", "observed=method npm"}
	if len(lines) != len(want) {
		t.Fatalf("want %d lines, got %d:\n%s", len(want), len(lines), verdict)
	}
	for i, prefix := range want {
		if !strings.HasPrefix(lines[i], prefix) || (prefix == "" && lines[i] != "") {
			t.Fatalf("line %d %q does not start with %q:\n%s", i+1, lines[i], prefix, verdict)
		}
	}
	if strings.ContainsRune(verdict, '\r') {
		t.Fatalf("a carriage return reached verdict.txt:\n%q", verdict)
	}
}

// TestPersistenceSetupStoresTheProbedAccountHash proves the setup stores the account hash that the
// in-sandbox probe printed, so the account name never reaches a file of the run, and fails when the
// probe prints no hash, through the fake executable.
func TestPersistenceSetupStoresTheProbedAccountHash(t *testing.T) {
	hash := strings.Repeat("d", 64)
	for name, c := range map[string]struct {
		shell  string
		passes bool
	}{
		"account selected": {"az exit 0\naz-account-sha256 " + hash + "\n", true},
		"no account":       {"az exit 1\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			f := fake(t, "sandboxed-agents", "podman")
			run := verifiedRun(t, f, f.path("sandboxed-agents"))
			f.script(t, "podman", map[string]response{"*": {}})
			f.script(t, "sandboxed-agents", map[string]response{
				"agents status chk-persist opencode": {Output: "opencode: enabled; version 1.2.3; pin none; session false; signed in unknown\n"},
				"shell chk-persist":                  {Output: c.shell},
				"*":                                  {},
			})
			err := run.PersistenceSetup(manualcheck.PersistenceSetupOptions{Sandbox: "chk-persist", GitHubRepo: "https://github.com/o/r", AzdoOrganization: "https://dev.azure.com/o", AzdoRepo: "https://dev.azure.com/o/p/_git/r"})
			config := readFile(t, filepath.Join(run.Root, "persistence", "config.json"))
			if c.passes && (err != nil || !strings.Contains(config, `"azure_account_sha256": "`+hash+`"`)) {
				t.Fatalf("%v\n%s", err, config)
			}
			if !c.passes && err == nil {
				t.Fatalf("a setup without an account hash passed:\n%s", config)
			}
		})
	}
}

// TestPersistenceObserveRefusesAConfigWithoutAccountHash proves a round refuses, before it runs
// anything in the sandbox, a persistence configuration whose setup noted no Azure account, so that
// an empty hash can never match every printed account hash, through the fake executable.
func TestPersistenceObserveRefusesAConfigWithoutAccountHash(t *testing.T) {
	f := fake(t, "sandboxed-agents", "podman")
	run := verifiedRun(t, f, f.path("sandboxed-agents"))
	dir := filepath.Join(run.Root, "persistence")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	config := `{"sandbox":"chk-persist","opencode_version":"1.19.3","github_repo":"https://github.com/o/r.git","azdo_organization":"https://dev.azure.com/o","azdo_repo":"https://dev.azure.com/o/p/_git/r","azure_account_sha256":"","setup_utc":"x"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	f.script(t, "sandboxed-agents", map[string]response{"*": {Output: "az exit 0\naz-account-sha256 " + strings.Repeat("e", 64) + "\n"}})
	err := run.PersistenceObserve(1)
	if err == nil || !strings.Contains(err.Error(), "no Azure account") {
		t.Fatalf("a configuration without an account hash was used: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "round-1")); !os.IsNotExist(statErr) {
		t.Fatal("the round ran before the refusal")
	}
	if calls := f.calls(t, "sandboxed-agents"); len(calls) != 0 {
		t.Fatalf("the round called the executable before the refusal: %v", calls)
	}
}

// TestPreflightWritesRunOnlyAfterChecks proves a preflight whose executable prints another version
// leaves no run.json, so it can be repeated with the same run ID, and that a verified preflight
// records the run with the inventory and the group proof, through fake tools.
func TestPreflightWritesRunOnlyAfterChecks(t *testing.T) {
	if runtimeIsWindowsHost() {
		t.Skip("the group proof uses a POSIX shell child on this path")
	}
	f := fake(t, "sandboxed-agents", "podman")
	parent := t.TempDir()
	options := manualcheck.PreflightOptions{Parent: parent, ID: "pre", Commit: strings.Repeat("a", 40), Version: "1.0.0", AssetSHA256: strings.Repeat("b", 64), Executable: f.path("sandboxed-agents")}
	f.script(t, "sandboxed-agents", map[string]response{"*": {}, "version": {Output: "sandboxed-agents 9.9.9\nasset-sha256 " + strings.Repeat("b", 64) + "\n"}, "list": {Output: "NAME STATE\n"}})
	if _, err := manualcheck.Preflight(options); err == nil {
		t.Fatal("a wrong version was verified")
	}
	if _, err := os.Stat(filepath.Join(parent, "pre", "run.json")); err == nil {
		t.Fatal("a failed preflight wrote run.json")
	}
	f.script(t, "sandboxed-agents", map[string]response{"*": {}, "version": {Output: "sandboxed-agents 1.0.0\nasset-sha256 " + strings.Repeat("b", 64) + "\n"}, "list": {Output: "NAME STATE\n"}})
	f.script(t, "podman", map[string]response{"*": {}, "info --format {{.Store.GraphRoot}}": {Output: parent + "\n"}})
	run, err := manualcheck.Preflight(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manualcheck.LoadRun(run.Root); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, filepath.Join(run.Root, "preflight", "group-proof.txt")), "group: manual-check") {
		t.Fatal("the group proof is missing")
	}
	if _, err := manualcheck.Preflight(options); err == nil {
		t.Fatal("a verified run was verified again under the same ID")
	}
}

// TestPersistenceAccountHashIgnoresTrailingNewline proves the setup's account hash equals the hash
// the in-sandbox round script computes with printf '%s' over the az output, so a name with a
// trailing newline compares equal, through the exported helper and a real shell pipeline.
func TestPersistenceAccountHashIgnoresTrailingNewline(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell")
	}
	output, err := exec.Command(shell, "-c", `printf '%s' "$(printf 'someone@example.invalid\n')" | sha256sum | cut -d' ' -f1`).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(output)) != manualcheck.AccountHash("someone@example.invalid\n") {
		t.Fatalf("shell hash %s differs from AccountHash", strings.TrimSpace(string(output)))
	}
}
