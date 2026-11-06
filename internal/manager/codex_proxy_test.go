package manager_test

import (
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
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "exec", "--skip-git-repo-check", "--ephemeral", "-c", "analytics.enabled=false", "-c", "features.plugins=false", "-c", "features.apps=false", "-c", "features.api_key_model_discovery=false", "-c", "features.api_key_cyber_access_programs=false", "Reply OK")
	cmd.Env = append(env, "CODEX_HOME="+dir)
	cmd.Dir = home
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native exec: %v %s", err, output)
	}
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
	e.Start = func(p manager.Process) error { return fmt.Errorf("private-key must not appear") }
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
				for _, env := range p.Environment {
					if stage, ok := strings.CutPrefix(env, "CODEX_HOME="); ok {
						return os.WriteFile(filepath.Join(stage, "auth.json"), []byte(`{"OPENAI_API_KEY":"private-key"}`), 0600)
					}
				}
				return fmt.Errorf("missing staged home")
			}
			configPublished, authAttempted := false, false
			e.RenameFile = func(from, to string) error {
				if to == authPath {
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
				err := os.Rename(from, to)
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
			got, err := os.ReadFile(authPath)
			if err != nil || string(got) != auth {
				t.Fatalf("prior login changed: %q (%v)", got, err)
			}
			info, err := os.Stat(authPath)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0644 {
				t.Fatalf("prior login mode changed: %v", info.Mode())
			}
		})
	}
}
