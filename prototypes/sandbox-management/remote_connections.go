package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// A connection keeps only metadata, a read-only last snapshot, and its private
// outbound client. Remote inventory is never installed in the local engine.
type remoteConnection struct {
	info   instanceInfo
	client *serviceClient
	cache  fakeState
	origin serviceInfo
}
type connectionRequest struct {
	Action string `json:"action"`
	ID     string `json:"id"`
	Label  string `json:"label"`
	URL    string `json:"url"`
	Token  string `json:"token"`
}

func emptyInventory() fakeState {
	return fakeState{Sandboxes: []sandbox{}, Images: []image{}, Events: []event{}, Busy: map[string]string{}, AgentCatalog: []agentInfo{}, Scenario: "normal"}
}
func (f *fakeFleet) hasInstance(id string) bool {
	if id == "local-fake" {
		return true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.remotes[id] != nil
}
func (f *fakeFleet) instanceInfos() []instanceInfo {
	local := f.hosts["local-fake"].snapshot()
	info := f.instances[0]
	info.SandboxCount = len(local.Sandboxes)
	info.ImageCount = len(local.Images)
	info.ServiceID = f.service.ID
	info.Group = local.ControllerGroup
	all := []instanceInfo{info}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.remoteOrder {
		all = append(all, f.remotes[id].info)
	}
	return all
}
func (f *fakeFleet) snapshot(id string) instanceState {
	state := emptyInventory()
	var origin *serviceInfo
	if id == "local-fake" {
		state = f.hosts[id].snapshot()
	} else {
		f.mu.Lock()
		remote := f.remotes[id]
		var c *serviceClient
		if remote != nil {
			c = remote.client
		}
		f.mu.Unlock()
		if c != nil {
			next, err := c.snapshot("local-fake")
			f.mu.Lock()
			if f.remotes[id] == remote && remote.client == c {
				if err != nil {
					f.remoteFailureLocked(remote, c, err)
				} else if next.Instance.ID != "local-fake" || next.Origin != nil || next.Service.ID != remote.info.ServiceID {
					f.remoteFailureLocked(remote, c, &apiError{401, "Remote service identity changed; reconnect with a new token"})
				} else {
					remote.cache = next.fakeState
					remote.origin = next.Service
					remote.info.Status = "connected"
					remote.info.Error = ""
					remote.info.SandboxCount = len(next.Sandboxes)
					remote.info.ImageCount = len(next.Images)
					remote.info.Group = next.ControllerGroup
				}
			}
			f.mu.Unlock()
		}

	}
	// Capture inventory, origin and connection metadata in the same generation.
	// No fleet lock is held while reading the local engine or doing network I/O.
	local := f.hosts["local-fake"].snapshot()
	info := f.instances[0]
	info.SandboxCount = len(local.Sandboxes)
	info.ImageCount = len(local.Images)
	info.ServiceID = f.service.ID
	info.Group = local.ControllerGroup
	all := []instanceInfo{info}
	selected := instanceInfo{ID: id, Status: "disconnected", Error: "Unknown instance"}
	f.mu.Lock()
	for _, remoteID := range f.remoteOrder {
		all = append(all, f.remotes[remoteID].info)
	}
	if id == "local-fake" {
		state = local
		selected = info
	} else if remote := f.remotes[id]; remote != nil {
		state = remote.cache
		selected = remote.info
		o := remote.origin
		origin = &o
	}
	f.mu.Unlock()
	return instanceState{fakeState: state, Instance: selected, Instances: all, Service: f.service, Origin: origin, Tokens: f.auth.list()}
}

func (f *fakeFleet) remoteFailureLocked(remote *remoteConnection, c *serviceClient, err error) {
	remote.info.Status = "offline"
	remote.info.Error = "Remote unavailable; cached inventory is read-only"
	var problem *apiError
	if errors.As(err, &problem) && problem.Code == 401 {
		remote.info.Status = "disconnected"
		remote.info.Error = "Remote token revoked, expired or invalid; reconnect with a replacement token"
		remote.client = nil
		c.close()
	}
}
func (f *fakeFleet) remoteClient(id string, write bool) (*serviceClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	remote := f.remotes[id]
	if remote == nil {
		return nil, &apiError{404, "Unknown instance"}
	}
	if remote.client == nil || write && remote.info.Status != "connected" {
		return nil, &apiError{409, "Remote disconnected or offline; reconnect before changing it"}
	}
	return remote.client, nil
}
func (f *fakeFleet) remoteFailure(id string, c *serviceClient, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	remote := f.remotes[id]
	if remote != nil && remote.client == c {
		f.remoteFailureLocked(remote, c, err)
	}
}
func (f *fakeFleet) changeConnection(input connectionRequest) (instanceInfo, error) {
	if input.Action == "disconnect" {
		f.mu.Lock()
		remote := f.remotes[input.ID]
		if remote == nil {
			f.mu.Unlock()
			return instanceInfo{}, fmt.Errorf("Unknown connection ID")
		}
		c := remote.client
		remote.client = nil
		remote.info.Status = "disconnected"
		remote.info.Error = "Disconnected; reconnect with a token"
		info := remote.info
		f.mu.Unlock()
		if c != nil {
			c.close()
		}
		return info, nil
	}
	if input.Action != "connect" && input.Action != "reconnect" {
		return instanceInfo{}, fmt.Errorf("Unknown connection action")
	}
	if strings.TrimSpace(input.Token) == "" {
		return instanceInfo{}, fmt.Errorf("A remote token is required")
	}
	label := strings.TrimSpace(input.Label)
	address := input.URL
	var expected *remoteConnection
	var previous *serviceClient
	if input.Action == "reconnect" {
		f.mu.Lock()
		remote := f.remotes[input.ID]
		if remote == nil {
			f.mu.Unlock()
			return instanceInfo{}, fmt.Errorf("Unknown connection ID")
		}
		label, address = remote.info.Name, remote.info.Address
		expected, previous = remote, remote.client
		f.mu.Unlock()
	}
	if label == "" || len(label) > 80 || cleanInput(label) != label {
		return instanceInfo{}, fmt.Errorf("Connection label must contain 1–80 printable characters")
	}
	c, err := newServiceClient(address, input.Token)
	if err != nil {
		return instanceInfo{}, err
	}
	c.localOnly = true
	keep := false
	defer func() {
		if !keep {
			c.close()
		}
	}()
	next, err := c.snapshot("local-fake")
	if err != nil {
		return instanceInfo{}, fmt.Errorf("Remote validation failed: valid token and reachable backend required")
	}
	if next.Instance.ID != "local-fake" || next.Origin != nil {
		return instanceInfo{}, fmt.Errorf("Recursive remote connections are not supported")
	}
	if next.Service.ID == f.service.ID {
		return instanceInfo{}, fmt.Errorf("Cannot connect a service to itself")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if input.Action == "reconnect" && (f.remotes[input.ID] != expected || expected.client != previous) {
		return instanceInfo{}, fmt.Errorf("Connection changed during validation; retry reconnect")
	}
	for id, remote := range f.remotes {
		if id != input.ID && (remote.info.ServiceID == next.Service.ID || remote.info.Address == c.address()) {
			return instanceInfo{}, fmt.Errorf("This remote environment is already registered")
		}
	}
	id := input.ID
	if input.Action == "connect" {
		if id != "" {
			return instanceInfo{}, fmt.Errorf("Connect must not specify an existing ID")
		}
		id = "remote-" + opaqueSecret()[:12]
	} else if remote := f.remotes[id]; remote == nil {
		return instanceInfo{}, fmt.Errorf("Unknown connection ID")
	}
	info := instanceInfo{ID: id, Name: label, Kind: "Remote backend / simulated inventory", Address: c.address(), Group: next.ControllerGroup, SandboxCount: len(next.Sandboxes), ImageCount: len(next.Images), SeedCount: next.Instance.SeedCount, Status: "connected", ServiceID: next.Service.ID}
	if old := f.remotes[id]; old != nil {
		if old.client != nil {
			old.client.close()
		}
	} else {
		f.remoteOrder = append(f.remoteOrder, id)
	}
	f.remotes[id] = &remoteConnection{info: info, client: c, cache: next.fakeState, origin: next.Service}
	keep = true
	return info, nil
}
func (f *fakeFleet) handleConnections(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		infos := f.instanceInfos()
		apiJSON(w, 200, infos[1:])
		return
	}
	var input connectionRequest
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		apiProblem(w, 400, "Invalid connection request")
		return
	}
	info, err := f.changeConnection(input)
	if err != nil {
		apiProblem(w, 409, err.Error())
		return
	}
	apiJSON(w, 200, info)
}

// proxyRemote forwards only inventory routes to the target's own local engine.
// A's authentication never leaves A, and a B auth failure is never an A 401.
func (f *fakeFleet) proxyRemote(w http.ResponseWriter, r *http.Request) bool {
	id := selectedInstance(r)
	if id == "local-fake" {
		return false
	}
	if r.URL.Path == "/api/state" {
		return false
	}
	switch r.URL.Path {
	case "/api/form", "/api/action", "/api/action-review", "/api/command", "/api/review", "/api/terminal":
	default:
		return false
	}
	c, err := f.remoteClient(id, r.Method != "GET" || r.URL.Path == "/api/terminal")
	if err != nil {
		var api *apiError
		errors.As(err, &api)
		apiProblem(w, api.Code, api.Message)
		return true
	}
	if r.URL.Path == "/api/terminal" {
		f.proxyTerminal(w, r, c, id)
		return true
	}
	u := c.endpoint(r.URL.Path, "local-fake")
	q := r.URL.Query()
	q.Set("instance", "local-fake")
	u.RawQuery = q.Encode()
	ctx, cancel := context.WithTimeout(r.Context(), 75*time.Second)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	req, err := http.NewRequestWithContext(ctx, r.Method, u.String(), http.MaxBytesReader(w, r.Body, 16384))
	if err != nil {
		apiProblem(w, 400, "Invalid remote request")
		return true
	}
	c.authorize(req)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		f.remoteFailure(id, c, err)
		apiProblem(w, 502, "Remote unavailable; no local fallback")
		return true
	}
	defer response.Body.Close()
	if response.StatusCode == 401 {
		f.remoteFailure(id, c, &apiError{401, "Remote authentication failed"})
		apiProblem(w, 409, "Remote token revoked, expired or invalid; reconnect with a replacement token")
		return true
	}
	if response.StatusCode != 200 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(response.Body, 2<<20))
		return true
	}
	if r.URL.Path == "/api/form" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.Copy(w, io.LimitReader(response.Body, 2<<20))
		return true
	}
	// Keep the A service identity and connection-scoped object identity in replies.
	apiJSON(w, 200, f.snapshot(id))
	return true
}
func terminalUpgrader(r *http.Request) websocket.Upgrader {
	protocols := []string{}
	if strings.HasPrefix(r.Header.Get("Sec-WebSocket-Protocol"), "sandbox-ticket.") {
		protocols = append(protocols, r.Header.Get("Sec-WebSocket-Protocol"))
	}
	return websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096, HandshakeTimeout: 5 * time.Second, Subprotocols: protocols, CheckOrigin: sameOrigin}
}
func (f *fakeFleet) proxyTerminal(w http.ResponseWriter, r *http.Request, c *serviceClient, id string) {
	u := c.endpoint("/api/terminal", "local-fake")
	q := u.Query()
	q.Set("sandbox", r.URL.Query().Get("sandbox"))
	q.Set("session", r.URL.Query().Get("session"))
	u.RawQuery = q.Encode()
	u.Scheme = "ws"
	if c.base.Scheme == "https" {
		u.Scheme = "wss"
	}
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	remote, response, err := dialer.DialContext(ctx, u.String(), http.Header{"Authorization": {"Bearer " + c.token}, "X-Prototype-Local-Only": {"1"}})
	if err != nil {
		if response != nil {
			response.Body.Close()
			if response.StatusCode == 401 {
				err = &apiError{401, "Remote authentication failed"}
			}
		}
		f.remoteFailure(id, c, err)
		apiProblem(w, 409, "Remote terminal unavailable; refresh or reconnect the environment")
		return
	}
	defer remote.Close()
	upgrader := terminalUpgrader(r)
	local, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer local.Close()
	done := make(chan struct{})
	defer close(done)
	g := r.Context().Value(grantKey{}).(*authGrant)
	go func() {
		select {
		case <-g.done:
		case <-c.ctx.Done():
		case <-done:
		}
		_ = remote.Close()
		_ = local.Close()
	}()
	copyStream := func(dst, src *websocket.Conn) {
		for {
			kind, data, e := src.ReadMessage()
			if e != nil {
				return
			}
			_ = dst.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if dst.WriteMessage(kind, data) != nil {
				return
			}
		}
	}
	remote.SetReadLimit(2 << 20)
	local.SetReadLimit(8192)
	go func() { defer local.Close(); defer remote.Close(); copyStream(remote, local) }()
	copyStream(local, remote)
}

func (f *fakeFleet) closeConnections() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, remote := range f.remotes {
		if remote.client != nil {
			remote.client.close()
			remote.client = nil
			remote.info.Status = "disconnected"
		}
	}
}
