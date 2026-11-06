package manager

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// proxyOwnership keeps nonsecret ownership and the prior model in the same atomic settings document.
type proxyOwnership struct {
	Version       int             `json:"version"`
	URLHash       string          `json:"urlHash"`
	TokenHash     string          `json:"tokenHash"`
	Model         json.RawMessage `json:"model,omitempty"`
	PreviousModel json.RawMessage `json:"previousModel,omitempty"`
}

const proxyMetadata = "_sandboxedAgentsProxy"

// parseProxyWorkflow validates proxy options without echoing caller-provided values in errors.
func parseProxyWorkflow(r agentRequest, args []string, stderr io.Writer) (agentRequest, bool) {
	options := map[string]string{}
	for i := 0; i < len(args); i++ {
		name, value, inline := strings.Cut(strings.TrimPrefix(args[i], "--"), "=")
		if !strings.HasPrefix(args[i], "--") || (name != "url" && name != "model" && name != "remove") {
			fmt.Fprintln(stderr, "unexpected proxy arguments")
			return r, false
		}
		if _, exists := options[name]; exists {
			fmt.Fprintln(stderr, "duplicate proxy option")
			return r, false
		}
		if name == "remove" {
			if inline {
				fmt.Fprintln(stderr, "--remove takes no value")
				return r, false
			}
			value = "true"
		} else if !inline {
			i++
			if i >= len(args) || strings.HasPrefix(args[i], "--") {
				fmt.Fprintln(stderr, "proxy option requires a value")
				return r, false
			}
			value = args[i]
		}
		options[name] = value
	}
	url, err := catalog.AgentProxyOptions(r.name, options)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return r, false
	}
	if options["remove"] == "" {
		options["url"] = url
	}
	r.proxyOptions = options
	return r, true
}

// proxyWorkflow serializes native configuration changes as the verified agent without spawning a credential-bearing process.
func (w agentWork) proxyWorkflow() int {
	unlock, err := lockAgentData(w.root)
	if err != nil {
		return exitStatus(err, w.stderr)
	}
	defer unlock()
	if _, err = w.requireEnabled(); err != nil {
		return exitStatus(err, w.stderr)
	}
	key := ""
	if w.request.proxyOptions["remove"] == "" {
		key, err = catalog.ReadProxyKey(w.effects.Stdin)
		if err != nil {
			return exitStatus(err, w.stderr)
		}
	}
	writer := writeClaudeProxy
	if w.request.name == "opencode" {
		writer = writeOpenCodeProxy
	}
	if err = writer(w.home, w.request.proxyOptions, key); err != nil {
		return exitStatus(err, w.stderr)
	}
	if w.request.name == "opencode" {
		if w.request.proxyOptions["remove"] != "" {
			fmt.Fprintln(w.stdout, "OpenCode proxy configuration removed; provider login is unchanged.")
		} else {
			fmt.Fprintln(w.stdout, "OpenCode proxy configuration written; the key was not checked against the server. Check with: sandboxed-agents agents run NAME opencode run 'Reply OK'. Restart existing sessions to load changed configuration.")
		}
		return 0
	}
	if w.request.proxyOptions["remove"] != "" {
		fmt.Fprintln(w.stdout, "Claude proxy configuration removed; browser login is unchanged.")
		return 0
	}
	fmt.Fprintln(w.stdout, "Claude proxy configuration written; the key was not checked against the server. Check with: sandboxed-agents agents run NAME claude -p 'Reply OK'. Restart existing sessions to load changed configuration.")
	return 0
}

// proxyDigest identifies an owned value without keeping another plaintext credential copy.
func proxyDigest(value json.RawMessage) string {
	var decoded string
	if json.Unmarshal(value, &decoded) != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(decoded)))
}

// writeClaudeProxy merges and restores only owned fields with one atomic native-settings replacement.
func writeClaudeProxy(home string, options map[string]string, key string) error {
	directory := filepath.Join(home, ".claude")
	path := filepath.Join(directory, "settings.json")
	for _, target := range []string{directory, path} {
		info, err := os.Lstat(target)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !agentFileOwned(info) || (target == directory && !info.IsDir()) || (target == path && !info.Mode().IsRegular()) {
			return errors.New("Claude settings require an agent-owned directory and regular unlinked file")
		}
	}
	settings := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return errors.New("cannot read Claude settings")
	}
	if err == nil && (json.Unmarshal(data, &settings) != nil || settings == nil) {
		return errors.New("malformed Claude settings.json")
	}
	env := map[string]json.RawMessage{}
	if raw, exists := settings["env"]; exists {
		if json.Unmarshal(raw, &env) != nil || env == nil {
			return errors.New("malformed Claude settings env")
		}
	}
	if _, exists := env["ANTHROPIC_API_KEY"]; exists {
		return errors.New("Claude settings contain conflicting env.ANTHROPIC_API_KEY")
	}
	if _, exists := settings["apiKeyHelper"]; exists {
		return errors.New("Claude settings contain conflicting apiKeyHelper")
	}
	var owned proxyOwnership
	if raw, exists := settings[proxyMetadata]; exists {
		if json.Unmarshal(raw, &owned) != nil || owned.Version != 1 {
			return errors.New("malformed Claude proxy ownership metadata")
		}
	}
	if options["remove"] != "" {
		if owned.Version == 0 {
			return nil
		}
		urlOwned := proxyDigest(env["ANTHROPIC_BASE_URL"]) == owned.URLHash
		tokenOwned := proxyDigest(env["ANTHROPIC_AUTH_TOKEN"]) == owned.TokenHash
		if _, tokenPresent := env["ANTHROPIC_AUTH_TOKEN"]; urlOwned && tokenPresent && !tokenOwned {
			return errors.New("Claude proxy key was edited; restore the managed key or remove ANTHROPIC_BASE_URL and ANTHROPIC_AUTH_TOKEN together in settings.json")
		}
		if urlOwned {
			delete(env, "ANTHROPIC_BASE_URL")
		}
		if tokenOwned {
			delete(env, "ANTHROPIC_AUTH_TOKEN")
		}
		if len(owned.Model) > 0 && equalProxyModel(settings["model"], owned.Model) {
			if len(owned.PreviousModel) > 0 {
				settings["model"] = owned.PreviousModel
			} else {
				delete(settings, "model")
			}
		}
		delete(settings, proxyMetadata)
	} else {
		env["ANTHROPIC_BASE_URL"], _ = json.Marshal(options["url"])
		env["ANTHROPIC_AUTH_TOKEN"], _ = json.Marshal(key)
		owned.Version = 1
		owned.URLHash = proxyDigest(env["ANTHROPIC_BASE_URL"])
		owned.TokenHash = proxyDigest(env["ANTHROPIC_AUTH_TOKEN"])
		if model, exists := options["model"]; exists {
			if len(owned.Model) == 0 || !equalProxyModel(settings["model"], owned.Model) {
				owned.PreviousModel = settings["model"]
			}
			owned.Model, _ = json.Marshal(model)
			settings["model"] = owned.Model
		}
		settings[proxyMetadata], _ = json.Marshal(owned)
	}
	settings["env"], _ = json.Marshal(env)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return errors.New("cannot prepare Claude settings directory")
	}
	if err := writeJSONFile(path, ".proxy-", settings); err != nil {
		return errors.New("cannot atomically write Claude settings")
	}
	return nil
}

// equalProxyModel compares model selections by decoded value, independent of JSON escaping.
func equalProxyModel(a, b json.RawMessage) bool {
	var left, right string
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && left == right
}
