package preflighttiming

import (
	"os"
	"sync"
	"time"
)

// Scope is one existing outer CLI call boundary.
type Scope string

// Preflight and TargetSelection are distinct intervals and are never added together.
const (
	Preflight       Scope = "preflight"
	TargetSelection Scope = "target-selection"
)

// Phase records an entered interval; seconds and outcome exist only after its call returns.
type Phase struct {
	State   string   `json:"state"`
	Outcome string   `json:"outcome,omitempty"`
	Seconds *float64 `json:"seconds,omitempty"`
}

// Invocation records only one process's local scope observations and dispatch return.
type Invocation struct {
	Version         int    `json:"version"`
	Run             string `json:"run"`
	Token           string `json:"token"`
	Preflight       *Phase `json:"preflight,omitempty"`
	TargetSelection *Phase `json:"target_selection,omitempty"`
	Returned        bool   `json:"returned,omitempty"`
}

// Observer keeps per-process observation state; nil is a filesystem-free no-op.
type Observer struct {
	mu     sync.Mutex
	root   *os.Root
	record Invocation
	closed bool
}

// Open activates only a valid per-invocation channel; an absent channel or malformed token touches no files.
func Open(getenv func(string) string) *Observer {
	dir, token := getenv(DirEnv), getenv(TokenEnv)
	if dir == "" || !tokenPattern.MatchString(token) {
		return nil
	}
	root, err := openDirectory(dir)
	if err != nil {
		return nil
	}
	m, err := readManifest(root)
	if err != nil {
		root.Close()
		return nil
	}
	o := &Observer{root: root, record: Invocation{Version: 1, Run: m.Run, Token: token}}
	_ = writeFile(root, "inv-"+token+".json", o.record)
	return o
}

// Observe returns the call's value and error unchanged, with observation I/O outside its interval.
func Observe[T any](o *Observer, scope Scope, call func() (T, error)) (T, error) {
	if o == nil {
		return call()
	}
	o.mu.Lock()
	var phase **Phase
	switch scope {
	case Preflight:
		phase = &o.record.Preflight
	case TargetSelection:
		phase = &o.record.TargetSelection
	default:
		o.mu.Unlock()
		return call()
	}
	repeated := *phase != nil
	if repeated {
		*phase = &Phase{State: "repeated"}
	} else {
		*phase = &Phase{State: "open"}
	}
	if !o.closed {
		_ = writeFile(o.root, "inv-"+o.record.Token+".json", o.record)
	}
	o.mu.Unlock()
	started := time.Now()
	value, err := call()
	seconds := time.Since(started).Seconds()
	o.mu.Lock()
	if !repeated && (*phase).State != "repeated" {
		outcome := "pass"
		if err != nil {
			outcome = "fail"
		}
		*phase = &Phase{State: "closed", Outcome: outcome, Seconds: &seconds}
	}
	if !o.closed {
		_ = writeFile(o.root, "inv-"+o.record.Token+".json", o.record)
	}
	o.mu.Unlock()
	return value, err
}

// Returned retains dispatch completion and closes the directory handle, ignoring all I/O errors.
func (o *Observer) Returned() {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	o.record.Returned = true
	_ = writeFile(o.root, "inv-"+o.record.Token+".json", o.record)
	o.closed = true
	_ = o.root.Close()
}
