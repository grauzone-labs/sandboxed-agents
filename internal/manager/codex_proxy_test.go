package manager_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// TestCodexProxyRequiresModel proves missing model fails before reading a key at RunWithEffects.
func TestCodexProxyRequiresModel(t *testing.T) {
	home, version, installs := t.TempDir(), "0.157.0", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex")
	e.Stdin = forbiddenProxyInput{t}
	out := runAgents(t, e, 2, "login", "codex", "proxy", "--url", "https://proxy.example")
	if !strings.Contains(out, "--model") {
		t.Fatalf("missing model diagnostic: %s", out)
	}
}

// TestCodexProxyRejectsOldVersion proves an unsupported pinned client is refused before key input at RunWithEffects.
func TestCodexProxyRejectsOldVersion(t *testing.T) {
	home, version, installs := t.TempDir(), "0.1.0", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex")
	e.Stdin = forbiddenProxyInput{t}
	out := runAgents(t, e, 1, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
	if !strings.Contains(out, "agents update") {
		t.Fatal(out)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); !os.IsNotExist(err) {
		t.Fatal("changed home")
	}
}

// TestCodexProxyRemoveAfterDowngrade proves removal clears an owned proxy after a downgrade and unblocks native login while setup stays version-gated through RunWithEffects.
func TestCodexProxyRemoveAfterDowngrade(t *testing.T) {
	e, dir := codexPublicationEffects(t)
	runAgents(t, e, 0, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
	version, installs := "0.1.0", 0
	installer := agentEffects(t, filepath.Dir(dir), &version, &installs)
	runAgents(t, installer, 0, "enable", "codex", "--version", version, "--force")
	e.Stdin = forbiddenProxyInput{t}
	if out := runAgents(t, e, 1, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen"); !strings.Contains(out, "agents update") {
		t.Fatalf("older setup lost its version gate: %s", out)
	}
	starts := 0
	e.Start = func(p manager.Process) error {
		if p.Program == "tmux" {
			return nil
		}
		starts++
		return nil
	}
	for _, workflow := range []string{"browser", "device-code"} {
		if out := runAgents(t, e, 1, "login", "codex", workflow); !strings.Contains(out, "proxy --remove") {
			t.Fatalf("proxy guard did not explain removal: %s", out)
		}
	}
	runAgents(t, e, 0, "login", "codex", "proxy", "--remove")
	if starts != 0 {
		t.Fatal("removal or guarded login started Codex")
	}
	config, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil || !strings.Contains(string(config), "original") || strings.Contains(string(config), "cliproxyapi") {
		t.Fatal("removal did not restore the original model and remove the owned provider")
	}
	if _, err := os.Stat(filepath.Join(dir, "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("owned proxy credential remains: %v", err)
	}
	for _, workflow := range []string{"browser", "device-code"} {
		runAgents(t, e, 0, "login", "codex", workflow)
	}
	if starts != 2 {
		t.Fatalf("native login did not resume: starts=%d", starts)
	}
}

// TestCodexProxyWritesNativeConfig proves managed configuration and private credentials are published through RunWithEffects.
func TestCodexProxyWritesNativeConfig(t *testing.T) {
	home, version, installs := t.TempDir(), "0.166.0", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex")
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("model = \"old\"\n[profiles.work]\nmodel = \"keep\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	e.Start = func(p manager.Process) error {
		if p.Program == "tmux" {
			return nil
		}
		if strings.Join(p.Args, " ") != "login --with-api-key -c cli_auth_credentials_store=\"file\"" {
			t.Fatalf("unexpected args %v", p.Args)
		}
		key, err := io.ReadAll(p.Stdin)
		if err != nil {
			return err
		}
		for _, env := range p.Environment {
			if strings.HasPrefix(env, "CODEX_HOME=") {
				data, _ := json.Marshal(map[string]string{"OPENAI_API_KEY": strings.TrimSpace(string(key))})
				return os.WriteFile(filepath.Join(strings.TrimPrefix(env, "CODEX_HOME="), "auth.json"), data, 0600)
			}
		}
		t.Fatal("missing isolated Codex home")
		return nil
	}
	e.Stdin = strings.NewReader("secret-key")
	out := runAgents(t, e, 0, "login", "codex", "proxy", "--url", "https://proxy.example/prefix", "--model", "chosen")
	if !strings.Contains(out, "not checked") {
		t.Fatal(out)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cliproxyapi", "https://proxy.example/prefix/v1", "chosen", "keep", "responses"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s: %s", want, data)
		}
	}
	if strings.Contains(string(data), "secret-key") {
		t.Fatal("key in TOML")
	}
	info, err := os.Stat(filepath.Join(dir, "auth.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("unsafe credentials: %v", err)
	}
}

// TestCodexProxyRefusalsPreserveNativeFiles proves malformed settings, foreign providers and ChatGPT login are refused before key input at RunWithEffects.
func TestCodexProxyRefusalsPreserveNativeFiles(t *testing.T) {
	for _, tc := range []struct{ name, config, auth, want string }{
		{"toml", "bad = [", `{}`, "malformed"},
		{"provider", "[model_providers.cliproxyapi]\nname = \"mine\"\n", `{}`, "not written"},
		{"chatgpt", "model = \"old\"\n", `{"tokens":{"access_token":"private"}}`, "--replace-login"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, version, installs := t.TempDir(), "0.166.0", 0
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, "enable", "codex")
			dir := filepath.Join(home, ".codex")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			for name, data := range map[string]string{"config.toml": tc.config, "auth.json": tc.auth} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			e.Stdin = forbiddenProxyInput{t}
			out := runAgents(t, e, 1, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
			if !strings.Contains(out, tc.want) {
				t.Fatal(out)
			}
			for name, want := range map[string]string{"config.toml": tc.config, "auth.json": tc.auth} {
				got, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(got) != want {
					t.Fatalf("changed %s", name)
				}
			}
		})
	}
}

// TestCodexProxyNativeClient proves setup, replacement, routing and removal through RunWithEffects and a real isolated Codex executable.
func TestCodexProxyNativeClient(t *testing.T) {
	binary := os.Getenv("SANDBOXED_AGENTS_TEST_CODEX")
	if binary == "" {
		t.Skip("set SANDBOXED_AGENTS_TEST_CODEX for native contract test")
	}
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != "POST" || r.URL.Path != "/prefix/v1/responses" {
			t.Errorf("unexpected request %s %s %s", r.Method, r.Host, r.URL.Path)
			http.Error(w, "forbidden", 403)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fake-proxy-key" {
			t.Error("wrong bearer")
		}
		var body struct{ Model string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "gpt-5.1-codex" {
			t.Errorf("model=%s", body.Model)
		}
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
		io.WriteString(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_test\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\",\"annotations\":[]}]}}\n\n")
		io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer server.Close()
	home, version, installs := t.TempDir(), "0.166.0", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex")
	env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "HTTP_PROXY=" + server.URL, "HTTPS_PROXY=" + server.URL, "ALL_PROXY=" + server.URL, "NO_PROXY=127.0.0.1,localhost"}
	e.Start = func(p manager.Process) error {
		if p.Program == "tmux" {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, p.Args...)
		cmd.Env = append(append([]string{}, env...), p.Environment...)
		cmd.Dir = home
		cmd.Stdin, cmd.Stdout, cmd.Stderr = p.Stdin, p.Stdout, p.Stderr
		return cmd.Run()
	}
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	original := "model = \"original\"\n[profiles.work]\nmodel = \"keep\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"tokens":{"access_token":"prior-chatgpt"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	e.Stdin = strings.NewReader("fake-proxy-key")
	runAgents(t, e, 0, "login", "codex", "proxy", "--url", server.URL+"/prefix", "--model", "gpt-5.1-codex", "--replace-login")
	output := runAgents(t, e, 0, "run", "codex", "exec", "--skip-git-repo-check", "--ephemeral", "-c", "analytics.enabled=false", "-c", "features.plugins=false", "-c", "features.apps=false", "-c", "features.api_key_model_discovery=false", "-c", "features.api_key_cyber_access_programs=false", "Reply OK")
	mu.Lock()
	count := requests
	mu.Unlock()
	if count == 0 {
		t.Fatalf("no responses request: %s", output)
	}
	e.Stdin = forbiddenProxyInput{t}
	runAgents(t, e, 0, "login", "codex", "proxy", "--remove")
	if _, err := os.Stat(filepath.Join(dir, "auth.json")); !os.IsNotExist(err) {
		t.Fatal("owned login remains")
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "original") || !strings.Contains(string(data), "keep") || strings.Contains(string(data), "cliproxyapi") || strings.Contains(string(data), "[model_providers]") {
		t.Fatalf("removal config: %s", data)
	}
}

// TestCodexProxyPreservesLaterLogin proves removal retains user-replaced API-key and ChatGPT credentials through RunWithEffects.
func TestCodexProxyPreservesLaterLogin(t *testing.T) {
	for _, later := range []string{`{"OPENAI_API_KEY":"later-key"}`, `{"tokens":{"access_token":"later-chatgpt"}}`} {
		home, version, installs := t.TempDir(), "0.166.0", 0
		e := agentEffects(t, home, &version, &installs)
		runAgents(t, e, 0, "enable", "codex")
		dir := filepath.Join(home, ".codex")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[model_providers]\n"), 0600); err != nil {
			t.Fatal(err)
		}

		e.Start = func(p manager.Process) error {
			if p.Program == "tmux" {
				return nil
			}
			for _, env := range p.Environment {
				if strings.HasPrefix(env, "CODEX_HOME=") {
					return os.WriteFile(filepath.Join(strings.TrimPrefix(env, "CODEX_HOME="), "auth.json"), []byte(`{"OPENAI_API_KEY":"managed-key"}`), 0600)
				}
			}
			return fmt.Errorf("missing home")
		}
		e.Stdin = strings.NewReader("managed-key")
		runAgents(t, e, 0, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
		path := filepath.Join(home, ".codex", "auth.json")
		if err := os.WriteFile(path, []byte(later), 0600); err != nil {
			t.Fatal(err)
		}
		e.Stdin = forbiddenProxyInput{t}
		runAgents(t, e, 0, "login", "codex", "proxy", "--remove")
		configuration, err := os.ReadFile(filepath.Join(dir, "config.toml"))
		if err != nil || !strings.Contains(string(configuration), "[model_providers]") {
			t.Fatal("preexisting empty table lost")
		}

		got, err := os.ReadFile(path)
		if err != nil || string(got) != later {
			t.Fatal("later login changed")
		}
		// Native workflows keep their original argv after removal.
		e.Start = func(p manager.Process) error {
			if p.Program == "tmux" {
				return nil
			}
			if strings.Join(p.Args, " ") != "login" && strings.Join(p.Args, " ") != "login --device-auth" {
				t.Fatalf("changed native workflow: %v", p.Args)
			}
			return nil
		}
		runAgents(t, e, 0, "login", "codex", "browser")
		runAgents(t, e, 0, "login", "codex", "device-code")
	}
}

// TestCodexProxyLoginFailureRestoresFiles proves a failed native login leaves configuration and login bytes and modes unchanged through RunWithEffects.
func TestCodexProxyLoginFailureRestoresFiles(t *testing.T) {
	home, version, installs := t.TempDir(), "0.166.0", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex")
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	originals := map[string]string{"config.toml": "# original\nmodel = \"original\"\n", "auth.json": `{"OPENAI_API_KEY":"prior-key"}`}
	for name, data := range originals {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	e.Start = func(p manager.Process) error {
		if p.Program == "tmux" {
			return nil
		}
		return fmt.Errorf("private-key must not appear")
	}
	e.Stdin = strings.NewReader("private-key")
	out := runAgents(t, e, 1, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
	if strings.Contains(out, "private-key") {
		t.Fatal("secret leaked")
	}
	for name, want := range originals {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Fatalf("changed %s", name)
		}
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0644 {
			t.Fatal("changed mode")
		}
	}
}

// TestCodexProxyBooleanDiagnostic proves a valued replacement flag names its own invalid option at RunWithEffects.
func TestCodexProxyBooleanDiagnostic(t *testing.T) {
	home, version, installs := t.TempDir(), "0.166.0", 0
	e := agentEffects(t, home, &version, &installs)
	e.Stdin = forbiddenProxyInput{t}
	out := runAgents(t, e, 2, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen", "--replace-login=true")
	if !strings.Contains(out, "--replace-login takes no value") {
		t.Fatal(out)
	}
}

// TestCodexProxyPublicationRollback proves authentication publication failure restores configuration bytes and mode, or reports restoration failure, through RunWithEffects.
func TestCodexProxyPublicationRollback(t *testing.T) {
	for _, restoreFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("restoreFails=%t", restoreFails), func(t *testing.T) {
			home, version, installs := t.TempDir(), "0.166.0", 0
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, "enable", "codex")
			dir := filepath.Join(home, ".codex")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			configPath, authPath := filepath.Join(dir, "config.toml"), filepath.Join(dir, "auth.json")
			original := "# preserve bytes\nmodel = \"original\"\n"
			auth := `{"OPENAI_API_KEY":"prior-key"}`
			for path, data := range map[string]string{configPath: original, authPath: auth} {
				if err := os.WriteFile(path, []byte(data), 0644); err != nil {
					t.Fatal(err)
				}
			}
			e.Start = func(p manager.Process) error {
				if p.Program == "tmux" {
					return nil
				}
				for _, env := range p.Environment {
					if stage, ok := strings.CutPrefix(env, "CODEX_HOME="); ok {
						return os.WriteFile(filepath.Join(stage, "auth.json"), []byte(`{"OPENAI_API_KEY":"private-key"}`), 0600)
					}
				}
				return fmt.Errorf("missing staged home")
			}
			configPublished, authAttempted := false, false
			e.LinkFile = func(from, to string) error {
				if to == authPath && !authAttempted {
					published, err := os.ReadFile(configPath)
					if err != nil || !strings.Contains(string(published), `model_provider = 'cliproxyapi'`) {
						t.Fatalf("configuration was not published before login: %s (%v)", published, err)
					}
					authAttempted = true
					return fmt.Errorf("private-key: injected authentication publication failure")
				}
				if to == configPath && configPublished && restoreFails {
					return fmt.Errorf("private-key: injected restoration failure")
				}
				err := os.Link(from, to)
				if to == configPath && err == nil {
					configPublished = true
				}
				return err
			}
			e.Stdin = strings.NewReader("private-key")
			out := runAgents(t, e, 1, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
			want := "cannot publish Codex login; previous configuration restored"
			if restoreFails {
				want = "Codex publication failed and configuration restore failed"
			}
			if !authAttempted || !strings.Contains(out, want) || strings.Contains(out, "private-key") {
				t.Fatalf("unexpected publication result: %s", out)
			}
			if !restoreFails {
				got, err := os.ReadFile(configPath)
				if err != nil || string(got) != original {
					t.Fatalf("configuration not restored: %q (%v)", got, err)
				}
				info, err := os.Stat(configPath)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0644 {
					t.Fatalf("configuration mode not restored: %v", info.Mode())
				}
			}
			authCheckPath := authPath
			if restoreFails {
				if _, err := os.Stat(authPath); !os.IsNotExist(err) {
					t.Fatalf("login remains active after failed configuration restoration: %v", err)
				}
				authCheckPath = filepath.Join(dir, ".sandboxed-agents-proxy", "auth.json.prev")
			}
			got, err := os.ReadFile(authCheckPath)
			if err != nil || string(got) != auth {
				t.Fatalf("prior login changed: %q (%v)", got, err)
			}
			info, err := os.Stat(authCheckPath)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0644 {
				t.Fatalf("prior login mode changed: %v", info.Mode())
			}
		})
	}
}

// TestCodexProxyGuardsNativeLogin proves browser and device-code login cannot replace credentials while the proxy is selected through RunWithEffects.
func TestCodexProxyGuardsNativeLogin(t *testing.T) {
	for _, tc := range []struct {
		name, config, remedy string
	}{
		{"managed", "model_provider = 'cliproxyapi'\n[_sandboxedAgentsProxy]\n", "proxy --remove"},
		{"unmanaged", "model_provider = 'cliproxyapi'\n", "manually remove the top-level model_provider selection"},
	} {
		for _, workflow := range []string{"browser", "device-code"} {
			t.Run(tc.name+"/"+workflow, func(t *testing.T) {
				home, version, installs := t.TempDir(), "0.166.0", 0
				e := agentEffects(t, home, &version, &installs)
				runAgents(t, e, 0, "enable", "codex")
				dir := filepath.Join(home, ".codex")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				originals := map[string]string{"config.toml": tc.config, "auth.json": `{"OPENAI_API_KEY":"private-key"}`}
				for name, data := range originals {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
						t.Fatal(err)
					}
				}
				started := false
				e.Start = func(p manager.Process) error { started = true; return nil }
				e.Stdin = forbiddenProxyInput{t}
				out := runAgents(t, e, 1, "login", "codex", workflow)
				if started || !strings.Contains(out, tc.remedy) || strings.Contains(out, "private-key") {
					t.Fatalf("unsafe native login: started=%t output=%s", started, out)
				}
				for name, want := range originals {
					got, err := os.ReadFile(filepath.Join(dir, name))
					if err != nil || string(got) != want {
						t.Fatalf("changed %s: %q (%v)", name, got, err)
					}
				}
			})
		}
	}
}

// TestCodexNativeLoginHoldsManagerLock proves proxy mutation cannot overlap a native login through RunWithEffects.
func TestCodexNativeLoginHoldsManagerLock(t *testing.T) {
	home, version, installs := t.TempDir(), "0.166.0", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex")
	started := false
	e.Start = func(p manager.Process) error {
		if p.Program == "tmux" {
			return nil
		}
		started = true
		competing := e
		competing.Stdin = forbiddenProxyInput{t}
		out := runAgents(t, competing, 1, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
		if !strings.Contains(out, "another agent or tool change is in progress; retry") {
			t.Fatalf("proxy mutation did not encounter manager lock: %s", out)
		}
		return nil
	}
	runAgents(t, e, 0, "login", "codex", "browser")
	if !started {
		t.Fatal("native login did not start")
	}
	e.Start = func(p manager.Process) error { return nil }
	runAgents(t, e, 0, "login", "codex", "device-code")
}

// TestCodexProxyPreservesEditsDuringLogin proves edits made while native login is staged are not overwritten by publication through RunWithEffects.
func TestCodexProxyPreservesEditsDuringLogin(t *testing.T) {
	home, version, installs := t.TempDir(), "0.166.0", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex")
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	configPath, authPath := filepath.Join(dir, "config.toml"), filepath.Join(dir, "auth.json")
	if err := os.WriteFile(configPath, []byte("model = 'original'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(`{"OPENAI_API_KEY":"original-key"}`), 0600); err != nil {
		t.Fatal(err)
	}
	laterConfig, laterAuth := "model = 'later'\n", `{"OPENAI_API_KEY":"later-private-key"}`
	e.Start = func(p manager.Process) error {
		if p.Program == "tmux" {
			return nil
		}
		if err := os.WriteFile(configPath, []byte(laterConfig), 0600); err != nil {
			return err
		}
		if err := os.WriteFile(authPath, []byte(laterAuth), 0600); err != nil {
			return err
		}
		for _, env := range p.Environment {
			if stage, ok := strings.CutPrefix(env, "CODEX_HOME="); ok {
				return os.WriteFile(filepath.Join(stage, "auth.json"), []byte(`{"OPENAI_API_KEY":"proxy-private-key"}`), 0600)
			}
		}
		return fmt.Errorf("missing staged home")
	}
	e.Stdin = strings.NewReader("proxy-private-key")
	out := runAgents(t, e, 1, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
	if strings.Contains(out, "private-key") {
		t.Fatal("credential leaked")
	}
	for path, want := range map[string]string{configPath: laterConfig, authPath: laterAuth} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("later edit overwritten: %s = %q (%v)", path, got, err)
		}
	}
}

// TestCodexProxyInterruptedPublicationFailsClosed proves interruption after destination publication leaves no old login and blocks managed Codex work through RunWithEffects.
func TestCodexProxyInterruptedPublicationFailsClosed(t *testing.T) {
	home, version, installs := t.TempDir(), "0.166.0", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex")
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	configPath, authPath := filepath.Join(dir, "config.toml"), filepath.Join(dir, "auth.json")
	original := "model = 'original'\n"
	if err := os.WriteFile(configPath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(`{"OPENAI_API_KEY":"old-private-key"}`), 0600); err != nil {
		t.Fatal(err)
	}
	e.Start = func(p manager.Process) error {
		if p.Program == "tmux" {
			return nil
		}
		for _, env := range p.Environment {
			if stage, ok := strings.CutPrefix(env, "CODEX_HOME="); ok {
				return os.WriteFile(filepath.Join(stage, "auth.json"), []byte(`{"OPENAI_API_KEY":"proxy-key"}`), 0600)
			}
		}
		return fmt.Errorf("missing staged home")
	}
	interrupted := false
	e.RenameFile = func(from, to string) error {
		if err := os.Rename(from, to); err != nil {
			return err
		}
		if to == configPath {
			panic("publication interrupted")
		}
		return nil
	}
	e.LinkFile = func(from, to string) error {
		if err := os.Link(from, to); err != nil {
			return err
		}
		if to == configPath {
			panic("publication interrupted")
		}
		return nil
	}
	e.Stdin = strings.NewReader("proxy-key")
	func() {
		defer func() {
			if recover() != nil {
				interrupted = true
			}
		}()
		runAgents(t, e, 0, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
	}()
	if !interrupted {
		t.Fatal("publication was not interrupted")
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatalf("old login remains beside changed destination: %v", err)
	}
	e.Start = func(p manager.Process) error { t.Fatal("Codex started with interrupted publication"); return nil }
	e.Stdin = forbiddenProxyInput{t}
	for _, args := range [][]string{{"run", "codex"}, {"session", "codex"}, {"login", "codex", "browser"}, {"login", "codex", "proxy", "--remove"}} {
		out := runAgents(t, e, 1, args...)
		if !strings.Contains(out, "recovery") || strings.Contains(out, "private-key") {
			t.Fatalf("missing safe recovery refusal: %s", out)
		}
	}
}

// codexPublicationEffects prepares real Codex files and a staged-login process effect for publication tests at RunWithEffects.
func codexPublicationEffects(t *testing.T) (manager.Effects, string) {
	t.Helper()
	home, version, installs := t.TempDir(), "0.166.0", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex")
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"config.toml": "model = 'original'\n", "auth.json": `{"OPENAI_API_KEY":"old-key"}`} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	e.Start = func(p manager.Process) error {
		if p.Program == "tmux" {
			return nil
		}
		for _, env := range p.Environment {
			if stage, ok := strings.CutPrefix(env, "CODEX_HOME="); ok {
				return os.WriteFile(filepath.Join(stage, "auth.json"), []byte(`{"OPENAI_API_KEY":"proxy-key"}`), 0600)
			}
		}
		return fmt.Errorf("missing staged home")
	}
	e.Stdin = strings.NewReader("proxy-key")
	return e, dir
}

// TestCodexProxyPublicationPreservesRacingEdits proves capture, no-clobber publication, and rollback retain direct edits through RunWithEffects.
func TestCodexProxyPublicationPreservesRacingEdits(t *testing.T) {
	for _, phase := range []string{"capture", "config publication", "auth publication", "rollback"} {
		t.Run(phase, func(t *testing.T) {
			e, dir := codexPublicationEffects(t)
			configPath, authPath := filepath.Join(dir, "config.toml"), filepath.Join(dir, "auth.json")
			later := "model = 'later-editor'\n"
			if phase == "capture" || phase == "auth publication" {
				later = `{"OPENAI_API_KEY":"later-private-key"}`
			}
			injected := false
			e.RenameFile = func(from, to string) error {
				if !injected && phase == "capture" && from == authPath {
					injected = true
					if err := os.WriteFile(from, []byte(later), 0600); err != nil {
						return err
					}
				}
				return os.Rename(from, to)
			}
			e.LinkFile = func(from, to string) error {
				if !injected && ((phase == "config publication" && to == configPath) || (phase == "auth publication" && to == authPath)) {
					injected = true
					if err := os.WriteFile(to, []byte(later), 0600); err != nil {
						return err
					}
				}
				if !injected && phase == "rollback" && to == authPath {
					injected = true
					if err := os.WriteFile(configPath, []byte(later), 0600); err != nil {
						return err
					}
					return fmt.Errorf("injected publication failure")
				}
				return os.Link(from, to)
			}
			out := runAgents(t, e, 1, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
			if !injected || strings.Contains(out, "later-private-key") {
				t.Fatalf("unsafe result: injected=%t output=%s", injected, out)
			}
			preserved := false
			err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() {
					return nil
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if string(data) == later {
					preserved = true
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !preserved {
				t.Fatal("concurrent edit lost from active and recovery files")
			}
		})
	}
}

// TestCodexProxyInterruptedUpdateAndRemovalKeepCredentialsInactive proves a changed or restored destination is never left with the old key after interrupted update or removal through RunWithEffects.
func TestCodexProxyInterruptedUpdateAndRemovalKeepCredentialsInactive(t *testing.T) {
	for _, operation := range []string{"update", "remove"} {
		t.Run(operation, func(t *testing.T) {
			e, dir := codexPublicationEffects(t)
			runAgents(t, e, 0, "login", "codex", "proxy", "--url", "https://old-proxy.example", "--model", "old-model")
			before, err := os.ReadFile(filepath.Join(dir, "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			e.LinkFile = func(from, to string) error {
				if err := os.Link(from, to); err != nil {
					return err
				}
				if to == filepath.Join(dir, "config.toml") {
					panic("publication interrupted")
				}
				return nil
			}
			e.Stdin = strings.NewReader("proxy-key")
			args := []string{"login", "codex", "proxy", "--url", "https://new-proxy.example", "--model", "new-model"}
			if operation == "remove" {
				args = []string{"login", "codex", "proxy", "--remove"}
				e.Stdin = forbiddenProxyInput{t}
			}
			interrupted := false
			func() {
				defer func() {
					if recover() != nil {
						interrupted = true
					}
				}()
				runAgents(t, e, 0, args...)
			}()
			if !interrupted {
				t.Fatal("publication not interrupted")
			}
			if _, err := os.Stat(filepath.Join(dir, "auth.json")); !os.IsNotExist(err) {
				t.Fatalf("old key active at changed destination: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(dir, ".sandboxed-agents-proxy", "config.toml.prev"))
			if err != nil || string(got) != string(before) {
				t.Fatal("immediate prior proxy configuration not preserved")
			}
			got, err = os.ReadFile(filepath.Join(dir, ".sandboxed-agents-proxy", "auth.json.prev"))
			if err != nil || !strings.Contains(string(got), "proxy-key") {
				t.Fatal("previous proxy login not preserved")
			}
			out := runAgents(t, e, 1, "login", "codex", "proxy", "--remove")
			if !strings.Contains(out, "recovery") {
				t.Fatalf("stale removal did not refuse: %s", out)
			}
		})
	}
}

// TestCodexProxyFirstPublicationFailureRestoresAbsence proves an authentication publication error leaves initially absent native files absent through RunWithEffects.
func TestCodexProxyFirstPublicationFailureRestoresAbsence(t *testing.T) {
	e, dir := codexPublicationEffects(t)
	for _, name := range []string{"config.toml", "auth.json"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	e.LinkFile = func(from, to string) error {
		if to == filepath.Join(dir, "auth.json") {
			return fmt.Errorf("injected auth failure")
		}
		return os.Link(from, to)
	}
	out := runAgents(t, e, 1, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
	if !strings.Contains(out, "previous configuration restored") {
		t.Fatal(out)
	}
	for _, name := range []string{"config.toml", "auth.json", ".sandboxed-agents-proxy"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected file after successful rollback: %s (%v)", name, err)
		}
	}
}

// TestCodexProxyCaptureAndAuthInterruptionsRetainRecovery proves interruption at either capture boundary or after auth publication retains safe files and refuses new runs through RunWithEffects.
func TestCodexProxyCaptureAndAuthInterruptionsRetainRecovery(t *testing.T) {
	for _, point := range []string{"auth capture", "config capture", "auth publication"} {
		t.Run(point, func(t *testing.T) {
			e, dir := codexPublicationEffects(t)
			configPath, authPath := filepath.Join(dir, "config.toml"), filepath.Join(dir, "auth.json")
			e.RenameFile = func(from, to string) error {
				if err := os.Rename(from, to); err != nil {
					return err
				}
				if point == "auth capture" && from == authPath || point == "config capture" && from == configPath {
					panic("interrupted capture")
				}
				return nil
			}
			e.LinkFile = func(from, to string) error {
				if err := os.Link(from, to); err != nil {
					return err
				}
				if point == "auth publication" && to == authPath {
					panic("interrupted auth publication")
				}
				return nil
			}
			interrupted := false
			func() {
				defer func() {
					if recover() != nil {
						interrupted = true
					}
				}()
				runAgents(t, e, 0, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
			}()
			if !interrupted {
				t.Fatal("interruption point not reached")
			}
			if point == "auth publication" {
				auth, err := os.ReadFile(authPath)
				if err != nil || string(auth) != `{"OPENAI_API_KEY":"proxy-key"}` {
					t.Fatal("published login differs from intended proxy key")
				}
				config, err := os.ReadFile(configPath)
				if err != nil || !strings.Contains(string(config), "https://proxy.example/v1") {
					t.Fatal("published login lacks matching destination")
				}
			} else if _, err := os.Stat(authPath); !os.IsNotExist(err) {
				t.Fatalf("captured old login remains active: %v", err)
			}
			e.Start = func(p manager.Process) error { t.Fatal("Codex started with incomplete publication"); return nil }
			out := runAgents(t, e, 1, "run", "codex")
			if !strings.Contains(out, "recovery") {
				t.Fatal(out)
			}
		})
	}
}

// TestCodexRunExcludesProxyChanges proves a managed run cannot read an old login across a proxy destination change through RunWithEffects.
func TestCodexRunExcludesProxyChanges(t *testing.T) {
	e, dir := codexPublicationEffects(t)
	writer := e
	reader := e
	reader.Start = func(p manager.Process) error {
		oldAuth, err := os.ReadFile(filepath.Join(dir, "auth.json"))
		if err != nil {
			t.Fatal(err)
		}
		var out, diagnostic bytes.Buffer
		status := manager.RunWithEffects([]string{"agents", "login", "codex", "proxy", "--url", "https://new-proxy.example", "--model", "chosen"}, &out, &diagnostic, writer)
		config, err := os.ReadFile(filepath.Join(dir, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		if status != 1 || !strings.Contains(diagnostic.String(), "Codex is running") || strings.Contains(string(config), "new-proxy.example") {
			t.Fatalf("proxy change overlapped a managed run with cached login %q: status=%d config=%s", oldAuth, status, config)
		}
		return nil
	}
	runAgents(t, reader, 0, "run", "codex")
	runAgents(t, writer, 0, "login", "codex", "proxy", "--url", "https://new-proxy.example", "--model", "chosen")
}
