package image_test

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/image"
)

// signingKeyCheck returns the paths of the key check script, the fingerprint files, and the key fixtures,
// skipping outside Linux and without bash and gpg, which the check needs.
func signingKeyCheck(t *testing.T) (checker string, fingerprints, fixtures map[string]string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the signing key check runs in the Debian image build")
	}
	for _, program := range []string{"bash", "gpg"} {
		if _, err := exec.LookPath(program); err != nil {
			t.Skipf("the signing key check needs %s", program)
		}
	}
	checker, err := filepath.Abs(filepath.Join("context", "base", "check-signing-key.sh"))
	if err != nil {
		t.Fatal(err)
	}
	fingerprints = map[string]string{
		"NodeSource": filepath.Join("context", "base", "nodesource-signing-key.fingerprint"),
		"Tailscale":  filepath.Join("context", "base", "tailscale-signing-key.fingerprint"),
		"Microsoft":  filepath.Join("context", "toolchains", "microsoft-signing-key.fingerprint"),
	}
	fixtures = map[string]string{
		"NodeSource": filepath.Join("test", "signing-keys", "nodesource-repo.gpg.key"),
		"Tailscale":  filepath.Join("test", "signing-keys", "tailscale-archive-keyring.gpg"),
		"Microsoft":  filepath.Join("test", "signing-keys", "microsoft.asc"),
	}
	return checker, fingerprints, fixtures
}

// runSigningKeyCheck runs the check script on a key file against a fingerprint file for a source name and
// returns its exit status, standard output, and standard error.
func runSigningKeyCheck(t *testing.T, checker string, args ...string) (int, string, string) {
	t.Helper()
	command := exec.Command("bash", append([]string{checker}, args...)...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	status := 0
	if exit, ok := err.(*exec.ExitError); ok {
		status = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return status, stdout.String(), stderr.String()
}

// readFingerprint reads a fingerprint file without its trailing newline.
func readFingerprint(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// TestSigningKeyCheckAcceptsEachPinnedKey proves every embedded fingerprint file accepts its repository's key
// fixture silently, through the actual check script with the real gpg.
func TestSigningKeyCheckAcceptsEachPinnedKey(t *testing.T) {
	checker, fingerprints, fixtures := signingKeyCheck(t)
	for source, fixture := range fixtures {
		status, stdout, stderr := runSigningKeyCheck(t, checker, fixture, fingerprints[source], source)
		if status != 0 || stdout != "" {
			t.Errorf("%s: status=%d stdout=%q stderr=%q; want silent acceptance", source, status, stdout, stderr)
		}
	}
}

// TestSigningKeyCheckRefusesOtherFingerprints proves a key checked against another repository's fingerprint, or a
// changed digit, is refused naming the source, the expected and the found fingerprint, through the actual check script.
func TestSigningKeyCheckRefusesOtherFingerprints(t *testing.T) {
	checker, fingerprints, fixtures := signingKeyCheck(t)
	expected := readFingerprint(t, fingerprints["NodeSource"])
	wrong := expected[:39] + "0"
	if strings.HasSuffix(expected, "0") {
		wrong = expected[:39] + "1"
	}
	wrongFile := filepath.Join(t.TempDir(), "wrong.fingerprint")
	if err := os.WriteFile(wrongFile, []byte(wrong+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, key, fingerprint, source, refusal string }{
		{"different digit", fixtures["NodeSource"], wrongFile, "NodeSource", "NodeSource signing key fingerprint mismatch: expected " + wrong + ", found " + expected},
		{"Tailscale keyring against NodeSource", fixtures["Tailscale"], fingerprints["NodeSource"], "Tailscale", "Tailscale signing key fingerprint mismatch: expected " + expected + ", found " + readFingerprint(t, fingerprints["Tailscale"])},
		{"NodeSource key against Tailscale", fixtures["NodeSource"], fingerprints["Tailscale"], "NodeSource", "expected " + readFingerprint(t, fingerprints["Tailscale"]) + ", found " + expected},
		{"Microsoft key against NodeSource", fixtures["Microsoft"], fingerprints["NodeSource"], "Microsoft", "Microsoft signing key fingerprint mismatch: expected " + expected + ", found " + readFingerprint(t, fingerprints["Microsoft"])},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, _, stderr := runSigningKeyCheck(t, checker, test.key, test.fingerprint, test.source)
			if status == 0 || !strings.Contains(stderr, test.refusal) {
				t.Fatalf("status=%d stderr=%q; want refusal %q", status, stderr, test.refusal)
			}
		})
	}
}

// TestSigningKeyCheckRefusesOtherThanOnePrimaryKey proves an empty file, a bundle of two known keys, and a bundle
// with a freshly generated key are refused with the count, the expected fingerprint, and every found fingerprint,
// and that the generated key alone is refused too, through the actual check script and the real gpg.
func TestSigningKeyCheckRefusesOtherThanOnePrimaryKey(t *testing.T) {
	checker, fingerprints, fixtures := signingKeyCheck(t)
	scratch := t.TempDir()
	expected := readFingerprint(t, fingerprints["NodeSource"])
	empty := filepath.Join(scratch, "empty.key")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	status, _, stderr := runSigningKeyCheck(t, checker, empty, fingerprints["NodeSource"], "NodeSource")
	for _, text := range []string{"expected exactly one primary key in " + empty + ", found 0", "expected fingerprint " + expected + ", found none"} {
		if status == 0 || !strings.Contains(stderr, text) {
			t.Fatalf("empty key: status=%d stderr=%q; want %q", status, stderr, text)
		}
	}
	bundle := filepath.Join(scratch, "microsoft-two-keys.asc")
	if err := os.WriteFile(bundle, concatenate(t, fixtures["Microsoft"], fixtures["NodeSource"]), 0600); err != nil {
		t.Fatal(err)
	}
	status, _, stderr = runSigningKeyCheck(t, checker, bundle, fingerprints["Microsoft"], "Microsoft")
	for _, text := range []string{"found 2", "expected fingerprint " + readFingerprint(t, fingerprints["Microsoft"])} {
		if status == 0 || !strings.Contains(stderr, text) {
			t.Fatalf("two known keys: status=%d stderr=%q; want %q", status, stderr, text)
		}
	}
	home := filepath.Join(scratch, "gnupg")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	generate := exec.Command("gpg", "--homedir", home, "--batch", "--passphrase", "", "--quick-gen-key", "test <test@example.invalid>", "ed25519", "default", "never")
	if output, err := generate.CombinedOutput(); err != nil {
		t.Skipf("gpg cannot generate a test key here: %v\n%s", err, output)
	}
	export := exec.Command("gpg", "--homedir", home, "--batch", "--armor", "--export", "test@example.invalid")
	exported, err := export.Output()
	if err != nil {
		t.Fatal(err)
	}
	additional := filepath.Join(scratch, "additional.key")
	if err := os.WriteFile(additional, exported, 0600); err != nil {
		t.Fatal(err)
	}
	listing, err := exec.Command("gpg", "--homedir", home, "--batch", "--with-colons", "--list-keys", "test@example.invalid").Output()
	if err != nil {
		t.Fatal(err)
	}
	generated := ""
	for _, line := range strings.Split(string(listing), "\n") {
		if fields := strings.Split(line, ":"); len(fields) > 10 && fields[0] == "fpr" {
			generated = fields[9]
			break
		}
	}
	if generated == "" {
		t.Fatalf("generated key fingerprint not listed:\n%s", listing)
	}
	twoKeys := filepath.Join(scratch, "two-keys.asc")
	if err := os.WriteFile(twoKeys, concatenate(t, fixtures["NodeSource"], additional), 0600); err != nil {
		t.Fatal(err)
	}
	status, _, stderr = runSigningKeyCheck(t, checker, twoKeys, fingerprints["NodeSource"], "NodeSource")
	for _, text := range []string{"expected exactly one primary key in " + twoKeys + ", found 2", "expected fingerprint " + expected + ", found " + expected + ", " + generated} {
		if status == 0 || !strings.Contains(stderr, text) {
			t.Fatalf("generated bundle: status=%d stderr=%q; want %q", status, stderr, text)
		}
	}
	status, _, stderr = runSigningKeyCheck(t, checker, additional, fingerprints["NodeSource"], "NodeSource")
	if status == 0 || !strings.Contains(stderr, "expected "+expected+", found "+generated) {
		t.Fatalf("generated key alone: status=%d stderr=%q; want its fingerprint refused", status, stderr)
	}
}

// TestSigningKeyCheckRequiresSourceName proves a call without the source name is a usage error through the actual
// check script.
func TestSigningKeyCheckRequiresSourceName(t *testing.T) {
	checker, fingerprints, fixtures := signingKeyCheck(t)
	status, _, stderr := runSigningKeyCheck(t, checker, fixtures["NodeSource"], fingerprints["NodeSource"])
	if status == 0 || !strings.Contains(stderr, "usage:") {
		t.Fatalf("status=%d stderr=%q; want a usage error", status, stderr)
	}
}

// TestSigningKeyFingerprintFilesAreCanonical proves every embedded fingerprint file holds exactly 40 uppercase hex
// characters and one line feed, through the embedded context.
func TestSigningKeyFingerprintFilesAreCanonical(t *testing.T) {
	canonical := regexp.MustCompile(`^[0-9A-F]{40}\n$`)
	for _, name := range []string{"base/nodesource-signing-key.fingerprint", "base/tailscale-signing-key.fingerprint", "toolchains/microsoft-signing-key.fingerprint"} {
		data, err := fs.ReadFile(image.Context(), name)
		if err != nil || !canonical.Match(data) {
			t.Errorf("%s = %q, error=%v; want 40 uppercase hex characters and one line feed", name, data, err)
		}
	}
}

// TestBaseRecipeChecksKeysBeforeWritingKeyrings proves the base recipe runs each repository's key check before it
// writes that repository's keyring and source list, through the embedded recipe's step order.
func TestBaseRecipeChecksKeysBeforeWritingKeyrings(t *testing.T) {
	data, err := fs.ReadFile(image.Context(), "base/Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	recipe := string(data)
	for _, test := range []struct{ name, check, keyring, source string }{
		{"NodeSource", `check-signing-key.sh "$key_file" /tmp/signing-key-check/nodesource-signing-key.fingerprint NodeSource`, "gpg --batch --yes --dearmor", "sources.list.d/nodesource.list"},
		{"Tailscale", `check-signing-key.sh "$key_file" /tmp/signing-key-check/tailscale-signing-key.fingerprint Tailscale`, `cp "$key_file" /etc/apt/keyrings/tailscale.gpg`, "sources.list.d/tailscale.list"},
	} {
		check, keyring, source := strings.Index(recipe, test.check), strings.Index(recipe, test.keyring), strings.Index(recipe, test.source)
		if check < 0 || keyring < 0 || source < 0 || check > keyring || check > source {
			t.Errorf("%s: check at %d, keyring at %d, source at %d; want the check first", test.name, check, keyring, source)
		}
	}
}

// concatenate returns the bytes of the named files joined in order.
func concatenate(t *testing.T, paths ...string) []byte {
	t.Helper()
	var joined []byte
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		joined = append(joined, data...)
	}
	return joined
}
