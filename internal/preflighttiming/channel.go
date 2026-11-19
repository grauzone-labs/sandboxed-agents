// Package preflighttiming owns a bounded private journal for two outer CLI observations.
package preflighttiming

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// DirEnv and TokenEnv identify the private run channel and one harness-issued invocation.
const (
	DirEnv   = "SANDBOXED_AGENTS_LIVE_PREFLIGHT_DIR"
	TokenEnv = "SANDBOXED_AGENTS_LIVE_PREFLIGHT_TOKEN"
)

// FileLimit and EntryLimit bound every journal artifact and directory scan.
const (
	FileLimit  = 4096
	EntryLimit = 8192
)

// Read errors have fixed identities; diagnostics never carry journal paths or payloads.
var (
	ErrUnavailable = errors.New("preflight timing channel unavailable")
	ErrManifest    = errors.New("preflight timing manifest invalid")
	ErrRevision    = errors.New("preflight timing revision mismatch")
)

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var runPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var tokenPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// manifest binds this fresh channel to one run and immutable source revision.
type manifest struct {
	Version  int    `json:"version"`
	Kind     string `json:"kind"`
	Run      string `json:"run"`
	Revision string `json:"revision"`
}

// randomHex creates an unguessable fixed-length local correlation token.
func randomHex(size int) (string, error) {
	b := make([]byte, size)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}

// openDirectory anchors access to a real absolute directory, refusing a link at the root.
func openDirectory(dir string) (*os.Root, error) {
	if !filepath.IsAbs(dir) {
		return nil, ErrUnavailable
	}
	before, err := os.Lstat(dir)
	if err != nil || !before.IsDir() || before.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return nil, ErrUnavailable
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, ErrUnavailable
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		root.Close()
		return nil, ErrUnavailable
	}
	return root, nil
}

// readFile rejects links and special/oversized files before a bounded strict JSON decode.
func readFile(root *os.Root, name string, out any) error {
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Size() > FileLimit {
		return ErrManifest
	}
	file, err := root.Open(name)
	if err != nil {
		return ErrManifest
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return ErrManifest
	}
	data, err := io.ReadAll(io.LimitReader(file, FileLimit+1))
	if err != nil || len(data) > FileLimit {
		return ErrManifest
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(out) != nil || dec.Decode(new(any)) != io.EOF {
		return ErrManifest
	}
	return nil
}

// readManifest validates the fixed channel identity without accepting extensions.
func readManifest(root *os.Root) (manifest, error) {
	var m manifest
	if readFile(root, "manifest.json", &m) != nil || m.Version != 1 || m.Kind != "live-preflight-timing" || !runPattern.MatchString(m.Run) || !revisionPattern.MatchString(m.Revision) {
		return m, ErrManifest
	}
	return m, nil
}

// entries bounds memory while listing the anchored channel, including abandoned temporary files.
func entries(root *os.Root) ([]os.DirEntry, error) {
	file, err := root.Open(".")
	if err != nil {
		return nil, ErrUnavailable
	}
	defer file.Close()
	names, err := file.ReadDir(EntryLimit + 1)
	if err != nil && err != io.EOF || len(names) > EntryLimit {
		return nil, ErrManifest
	}
	return names, nil
}

// writeFile publishes one bounded regular artifact by replacing a same-directory temporary file.
func writeFile(root *os.Root, name string, value any) error {
	if names, err := entries(root); err != nil || len(names) >= EntryLimit {
		return ErrManifest
	}
	if info, err := root.Lstat(name); err == nil && !info.Mode().IsRegular() {
		return ErrManifest
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrManifest
	}
	data, err := json.Marshal(value)
	if err != nil || len(data)+1 > FileLimit {
		return ErrManifest
	}
	token, err := randomHex(16)
	if err != nil {
		return ErrUnavailable
	}
	temporary := ".tmp-" + token
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrUnavailable
	}
	defer root.Remove(temporary)
	_, err = file.Write(append(data, '\n'))
	closed := file.Close()
	if err != nil || closed != nil {
		return ErrUnavailable
	}
	return root.Rename(temporary, name)
}

// Create creates one fresh channel without querying an engine or reusing old observations.
func Create(runDir, revision string) (string, error) {
	if !revisionPattern.MatchString(revision) {
		return "", ErrRevision
	}
	parent, err := openDirectory(runDir)
	if err != nil {
		return "", err
	}
	defer parent.Close()
	if err = parent.Mkdir("preflight-timing", 0700); err != nil {
		return "", ErrUnavailable
	}
	root, err := parent.OpenRoot("preflight-timing")
	if err != nil {
		return "", ErrUnavailable
	}
	defer root.Close()
	run, err := randomHex(32)
	if err != nil {
		return "", ErrUnavailable
	}
	if err = writeFile(root, "manifest.json", manifest{1, "live-preflight-timing", run, revision}); err != nil {
		return "", err
	}
	return filepath.Join(runDir, "preflight-timing"), nil
}
