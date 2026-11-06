package manager

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
	"github.com/pelletier/go-toml/v2"
)

// codexProxyOwnership records previous selections and hashes without duplicating credentials.
type codexProxyOwnership struct {
	Provider         map[string]any
	PreviousProvider any
	PreviousModel    any
	TokenHash        string
	ProvidersExisted bool
}

// codexProxyWorkflow stages native login before publishing configuration and credentials under the manager lock.
func (w agentWork) codexProxyWorkflow() error {
	dir := filepath.Join(w.home, ".codex")
	configPath, authPath := filepath.Join(dir, "config.toml"), filepath.Join(dir, "auth.json")
	if err := validateProxyFiles("Codex", dir, configPath, authPath); err != nil {
		return err
	}
	before, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return errors.New("cannot read Codex config")
	}
	auth, err := os.ReadFile(authPath)
	if err != nil && !os.IsNotExist(err) {
		return errors.New("cannot read Codex login")
	}
	config := map[string]any{}
	if len(before) > 0 {
		if err := toml.Unmarshal(before, &config); err != nil {
			return errors.New("malformed Codex config.toml")
		}
	}
	if store, ok := config["cli_auth_credentials_store"]; ok && store != "file" {
		return errors.New("Codex proxy requires cli_auth_credentials_store = file")
	}
	_, providersExisted := config["model_providers"]
	providers := map[string]any{}
	if value, exists := config["model_providers"]; exists {
		var ok bool
		providers, ok = value.(map[string]any)
		if !ok {
			return errors.New("malformed Codex model providers")
		}
	}
	var owned codexProxyOwnership
	if raw, exists := config[proxyMetadata]; exists {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return errors.New("malformed Codex proxy ownership")
		}
		if json.Unmarshal(encoded, &owned) != nil || owned.Provider == nil || owned.TokenHash == "" {
			return errors.New("malformed Codex proxy ownership")
		}
	}
	if provider, exists := providers["cliproxyapi"]; exists && (owned.Provider == nil || !reflect.DeepEqual(provider, owned.Provider)) {
		return errors.New("existing cliproxyapi provider was not written by this workflow")
	}
	credentials := map[string]any{}
	if len(auth) > 0 && json.Unmarshal(auth, &credentials) != nil {
		return errors.New("malformed Codex stored login")
	}
	remove := w.request.proxyOptions["remove"] != ""
	if remove && owned.Provider == nil {
		return nil
	}
	newAuth := auth
	if remove {
		delete(providers, "cliproxyapi")
		if config["model_provider"] == "cliproxyapi" {
			restoreCodexValue(config, "model_provider", owned.PreviousProvider)
			restoreCodexValue(config, "model", owned.PreviousModel)
		}
		if key, ok := credentials["OPENAI_API_KEY"].(string); ok && key != "" && codexKeyHash(key) == owned.TokenHash && credentials["tokens"] == nil && credentials["auth_mode"] != "chatgpt" {
			newAuth = nil
		}
		delete(config, proxyMetadata)
	} else {
		if credentials["tokens"] != nil || credentials["auth_mode"] == "chatgpt" {
			if w.request.proxyOptions["replace-login"] == "" {
				return errors.New("Codex has a ChatGPT login; use --replace-login to replace it")
			}
		}
		key, err := catalog.ReadProxyKey(w.effects.Stdin)
		if err != nil {
			return err
		}
		// A separate CODEX_HOME keeps a failed native login from changing the active login.
		stage, err := os.MkdirTemp(w.home, ".codex-proxy-")
		if err != nil {
			return errors.New("cannot stage Codex login")
		}
		defer os.RemoveAll(stage)
		err = w.effects.Start(Process{Program: commandPath(w.home, w.request.definition), Args: []string{"login", "--with-api-key", "-c", `cli_auth_credentials_store="file"`}, Directory: w.home, Identity: agentIdentity, Environment: []string{"CODEX_HOME=" + stage}, Stdin: strings.NewReader(key + "\n"), Stdout: io.Discard, Stderr: io.Discard})
		if err != nil {
			return errors.New("Codex API-key login failed; previous configuration and login unchanged")
		}
		newAuth, err = os.ReadFile(filepath.Join(stage, "auth.json"))
		if err != nil {
			return errors.New("Codex did not store an API-key login")
		}
		var stored map[string]any
		if json.Unmarshal(newAuth, &stored) != nil || stored["OPENAI_API_KEY"] != key || stored["tokens"] != nil {
			return errors.New("Codex stored an unexpected login")
		}
		if owned.Provider == nil || config["model_provider"] != "cliproxyapi" {
			if owned.Provider == nil {
				owned.ProvidersExisted = providersExisted
			}
			owned.PreviousProvider = config["model_provider"]
			owned.PreviousModel = config["model"]
		}
		owned.Provider = map[string]any{"name": "CLIProxyAPI", "base_url": w.request.proxyOptions["url"] + "/v1", "wire_api": "responses", "requires_openai_auth": true}
		owned.TokenHash = codexKeyHash(key)
		providers["cliproxyapi"] = owned.Provider
		config["model_provider"] = "cliproxyapi"
		config["model"] = w.request.proxyOptions["model"]
		// JSON round-tripping omits absent previous fields before TOML serialization.
		metadata := map[string]any{"Provider": owned.Provider, "TokenHash": owned.TokenHash, "ProvidersExisted": owned.ProvidersExisted}
		if owned.PreviousProvider != nil {
			metadata["PreviousProvider"] = owned.PreviousProvider
		}
		if owned.PreviousModel != nil {
			metadata["PreviousModel"] = owned.PreviousModel
		}
		config[proxyMetadata] = metadata
	}
	if remove && len(providers) == 0 && !owned.ProvidersExisted {
		delete(config, "model_providers")
	} else {
		config["model_providers"] = providers
	}
	after, err := toml.Marshal(config)
	if err != nil {
		return errors.New("cannot encode Codex configuration")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.New("cannot prepare Codex directory")
	}
	if err := publishCodexFiles(configPath, authPath, before, auth, after, newAuth, w.effects); err != nil {
		return err
	}
	return nil
}

// restoreCodexValue restores the prior selection or its absence.
func restoreCodexValue(config map[string]any, key string, value any) {
	if value == nil {
		delete(config, key)
	} else {
		config[key] = value
	}
}

// codexKeyHash identifies the credential still owned by the workflow.
func codexKeyHash(key string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(key))) }

// guardCodexNativeLogin refuses account login while the proxy destination is selected, or its configuration cannot be safely read.
func (w agentWork) guardCodexNativeLogin() error {
	dir := filepath.Join(w.home, ".codex")
	path := filepath.Join(dir, "config.toml")
	if err := validateProxyFiles("Codex", dir, path); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errors.New("cannot read Codex config")
	}
	config := map[string]any{}
	if toml.Unmarshal(data, &config) != nil {
		return errors.New("malformed Codex config.toml")
	}
	if _, owned := config[proxyMetadata]; owned {
		return errors.New("Codex proxy is configured; run agents login NAME codex proxy --remove before browser or device-code login")
	}
	if config["model_provider"] == "cliproxyapi" {
		return errors.New("Codex cliproxyapi selection is unmanaged; manually remove the top-level model_provider selection from ~/.codex/config.toml before browser or device-code login")
	}
	return nil
}
