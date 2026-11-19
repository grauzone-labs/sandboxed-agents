package preflighttiming

import (
	"os"
	"sync"
)

// Launch records one attempted child process without retaining its command or identity.
type Launch struct {
	Version   int      `json:"version"`
	Run       string   `json:"run"`
	Token     string   `json:"token"`
	Sequence  int      `json:"sequence"`
	Case      string   `json:"case"`
	Operation string   `json:"operation"`
	Result    string   `json:"result,omitempty"`
	Seconds   *float64 `json:"seconds,omitempty"`
}

// Ledger correlates concurrent child launches and retains a sticky observation-loss signal.
type Ledger struct {
	mu         sync.Mutex
	root       *os.Root
	manifest   manifest
	sequence   int
	pending    map[string]Launch
	unverified bool
}

// OpenLedger requires a fresh correctly bound channel before the harness issues tokens.
func OpenLedger(dir, revision string) (*Ledger, error) {
	root, err := openDirectory(dir)
	if err != nil {
		return nil, err
	}
	m, err := readManifest(root)
	if err != nil {
		root.Close()
		return nil, err
	}
	if m.Revision != revision {
		root.Close()
		return nil, ErrRevision
	}
	names, err := entries(root)
	if err != nil || len(names) != 1 || names[0].Name() != "manifest.json" {
		root.Close()
		return nil, ErrManifest
	}
	return &Ledger{root: root, manifest: m, pending: map[string]Launch{}}, nil
}

// Launch publishes a process token before starting the child; an I/O failure
// still returns the token so a producer can preserve an unattributed observation.
func (l *Ledger) Launch(caseName, operation string) (string, string, error) {
	if l == nil {
		return "", "", ErrUnavailable
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	token, err := randomHex(16)
	if err != nil {
		l.unverified = true
		return "", "", err
	}
	l.sequence++
	launch := Launch{Version: 1, Run: l.manifest.Run, Token: token, Sequence: l.sequence, Case: caseName, Operation: operation}
	if l.sequence > EntryLimit || len(caseName) > 64 || len(operation) > 64 {
		l.unverified = true
		return "", "", ErrManifest
	}
	l.pending[token] = launch
	err = writeFile(l.root, "launch-"+token+".json", launch)
	if err != nil {
		l.unverified = true
	}
	return TokenEnv + "=" + token, token, err
}

// Finish rewrites one launch exactly once with the same inclusive seconds used
// by the existing operation record; failure never changes a child result.
func (l *Ledger) Finish(token, result string, seconds float64) {
	if l == nil || token == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	launch, ok := l.pending[token]
	if !ok || !ValidCompletion(result, &seconds) {
		l.unverified = true
		return
	}
	delete(l.pending, token)
	launch.Result = result
	launch.Seconds = &seconds
	if writeFile(l.root, "launch-"+token+".json", launch) != nil {
		l.unverified = true
	}
}

// Unverified reports sticky ledger loss, including a final launch whose files
// could not be retained, so the collector cannot silently call the run complete.
func (l *Ledger) Unverified() bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.unverified
}

// Close releases the private directory handle after all case cleanup has ended.
func (l *Ledger) Close() {
	if l != nil {
		l.mu.Lock()
		defer l.mu.Unlock()
		_ = l.root.Close()
	}
}
