package main

// THROWAWAY: all tokens grant full administrative access and live in memory.
// Issued secrets are returned only by creation; the vault retains their hashes.
import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type tokenInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Created string `json:"created"`
	Expires string `json:"expires"`
	Status  string `json:"status"`
}
type authGrant struct {
	info    tokenInfo
	hash    [32]byte
	expires time.Time
	done    chan struct{}
	revoked bool
}
type terminalTicket struct {
	grant                      *authGrant
	instance, sandbox, session string
	expires                    time.Time
}
type authVault struct {
	mu      sync.Mutex
	grants  map[[32]byte]*authGrant
	tokens  []*authGrant
	tickets map[[32]byte]terminalTicket
}
type grantKey struct{}

func opaqueSecret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("Cannot generate secure prototype credential")
	}
	return hex.EncodeToString(b[:])
}
func newAuthVault() (*authVault, string) {
	secret := opaqueSecret()
	g := &authGrant{info: tokenInfo{ID: "owner"}, hash: sha256.Sum256([]byte(secret)), done: make(chan struct{})}
	return &authVault{grants: map[[32]byte]*authGrant{g.hash: g}, tickets: map[[32]byte]terminalTicket{}}, secret
}
func (a *authVault) issue(name string, seconds int) (tokenInfo, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 || cleanInput(name) != name {
		return tokenInfo{}, "", fmt.Errorf("Token name must contain 1–80 printable characters")
	}
	if seconds == 0 {
		seconds = 24 * 60 * 60
	}
	if seconds < 1 || seconds > 365*24*60*60 {
		return tokenInfo{}, "", fmt.Errorf("Token lifetime must be between 1 second and 365 days")
	}
	secret := opaqueSecret()
	now := time.Now().UTC()
	expires := now.Add(time.Duration(seconds) * time.Second)
	g := &authGrant{info: tokenInfo{ID: opaqueSecret()[:16], Name: name, Created: now.Format(time.RFC3339), Expires: expires.Format(time.RFC3339), Status: "active"}, hash: sha256.Sum256([]byte(secret)), expires: expires, done: make(chan struct{})}
	a.mu.Lock()
	a.grants[g.hash] = g
	a.tokens = append(a.tokens, g)
	a.mu.Unlock()
	time.AfterFunc(time.Until(expires), func() { a.mu.Lock(); defer a.mu.Unlock(); a.invalidate(g, false) })
	return g.info, secret, nil
}
func (a *authVault) invalidate(g *authGrant, revoked bool) {
	if revoked {
		g.revoked = true
	}
	select {
	case <-g.done:
	default:
		close(g.done)
	}
}
func (a *authVault) valid(g *authGrant) bool {
	if g == nil || g.revoked {
		return false
	}
	if !g.expires.IsZero() && !time.Now().Before(g.expires) {
		a.invalidate(g, false)
		return false
	}
	return true
}
func (a *authVault) authenticate(secret string) *authGrant {
	a.mu.Lock()
	defer a.mu.Unlock()
	g := a.grants[sha256.Sum256([]byte(secret))]
	if !a.valid(g) {
		return nil
	}
	return g
}
func (a *authVault) list() []tokenInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := []tokenInfo{}
	for _, g := range a.tokens {
		info := g.info
		if g.revoked {
			info.Status = "revoked"
		} else if !a.valid(g) {
			info.Status = "expired"
		}
		result = append(result, info)
	}
	return result
}
func (a *authVault) revoke(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, g := range a.tokens {
		if g.info.ID == id {
			a.invalidate(g, true)
			return nil
		}
	}
	return fmt.Errorf("Unknown token ID")
}
func apiJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func apiProblem(w http.ResponseWriter, code int, message string) {
	apiJSON(w, code, map[string]string{"error": message})
}
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	u, err := url.Parse(origin)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return err == nil && u.Scheme == scheme && strings.EqualFold(u.Host, r.Host) && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}
func (f *fakeFleet) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if !sameOrigin(r) {
			apiProblem(w, 403, "Foreign origin is not allowed")
			return
		}
		var g *authGrant
		if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
			g = f.auth.authenticate(strings.TrimPrefix(header, "Bearer "))
		}
		if g == nil && r.URL.Path == "/api/terminal" && r.Method == "GET" {
			protocols := strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",")
			if len(protocols) == 1 && strings.HasPrefix(strings.TrimSpace(protocols[0]), "sandbox-ticket.") {
				g = f.consumeTicket(strings.TrimPrefix(strings.TrimSpace(protocols[0]), "sandbox-ticket."), r)
			}
		}
		if g == nil {
			apiProblem(w, 401, "Valid token required; token may be missing, revoked or expired")
			return
		}
		if r.Header.Get("X-Prototype-Local-Only") == "1" && selectedInstance(r) != "local-fake" {
			apiProblem(w, 409, "Recursive remote connections are not supported")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), grantKey{}, g)))
	})
}
func selectedInstance(r *http.Request) string {
	id := r.URL.Query().Get("instance")
	if id == "" {
		return "local-fake"
	}
	return id
}
func (f *fakeFleet) handleTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		apiJSON(w, 200, f.auth.list())
		return
	}
	var input struct {
		Action           string `json:"action"`
		Name             string `json:"name"`
		ID               string `json:"id"`
		ExpiresInSeconds int    `json:"expiresInSeconds"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		apiProblem(w, 400, "Invalid token request")
		return
	}
	if input.Action == "revoke" {
		if err := f.auth.revoke(input.ID); err != nil {
			apiProblem(w, 404, err.Error())
			return
		}
		apiJSON(w, 200, map[string]string{"status": "revoked"})
		return
	}
	if input.Action != "create" {
		apiProblem(w, 400, "Unknown token action")
		return
	}
	info, secret, err := f.auth.issue(input.Name, input.ExpiresInSeconds)
	if err != nil {
		apiProblem(w, 400, err.Error())
		return
	}
	apiJSON(w, 200, struct {
		Token  tokenInfo `json:"token"`
		Secret string    `json:"secret"`
	}{info, secret})
}
func (f *fakeFleet) handleTicket(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Sandbox string `json:"sandbox"`
		Session string `json:"session"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if json.NewDecoder(r.Body).Decode(&input) != nil || input.Sandbox == "" || input.Session == "" {
		apiProblem(w, 400, "Sandbox and session are required")
		return
	}
	id := selectedInstance(r)
	if !f.hasInstance(id) {
		apiProblem(w, 404, "Unknown instance")
		return
	}
	secret := opaqueSecret()
	f.auth.mu.Lock()
	for hash, t := range f.auth.tickets {
		if time.Now().After(t.expires) {
			delete(f.auth.tickets, hash)
		}
	}
	f.auth.tickets[sha256.Sum256([]byte(secret))] = terminalTicket{r.Context().Value(grantKey{}).(*authGrant), id, input.Sandbox, input.Session, time.Now().Add(15 * time.Second)}
	f.auth.mu.Unlock()
	apiJSON(w, 200, map[string]string{"ticket": secret})
}
func (f *fakeFleet) consumeTicket(secret string, r *http.Request) *authGrant {
	a := f.auth
	a.mu.Lock()
	defer a.mu.Unlock()
	hash := sha256.Sum256([]byte(secret))
	ticket, ok := a.tickets[hash]
	delete(a.tickets, hash)
	if !ok || !time.Now().Before(ticket.expires) || !a.valid(ticket.grant) || ticket.instance != selectedInstance(r) || ticket.sandbox != r.URL.Query().Get("sandbox") || ticket.session != r.URL.Query().Get("session") {
		return nil
	}
	return ticket.grant
}
