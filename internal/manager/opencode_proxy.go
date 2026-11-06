package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// openCodeProxyOwnership identifies the managed provider and remembers the model selected before it.
type openCodeProxyOwnership struct {
	Version       int             `json:"version"`
	PreviousModel json.RawMessage `json:"previousModel,omitempty"`
}

// writeOpenCodeProxy atomically merges a dedicated provider, keeping its nonsecret ownership marker in extensible provider options.
func writeOpenCodeProxy(home string, options map[string]string, key string) error {
	directory := filepath.Join(home, ".config", "opencode")
	path := filepath.Join(directory, "opencode.json")
	fail := func(cause string) error { return fmt.Errorf("%s: %s", path, cause) }
	for _, target := range []string{filepath.Join(home, ".config"), directory, path} {
		info, err := os.Lstat(target)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !agentFileOwned(info) || (target != path && !info.IsDir()) || (target == path && !info.Mode().IsRegular()) {
			return fail("requires agent-owned directories and a regular unlinked file")
		}
	}
	jsonc := filepath.Join(directory, "opencode.jsonc")
	if _, err := os.Lstat(jsonc); !os.IsNotExist(err) {
		return fmt.Errorf("%s: JSONC configuration is not supported", jsonc)
	}
	settings := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fail("cannot read configuration")
	}
	if err == nil && (json.Unmarshal(data, &settings) != nil || settings == nil) {
		return fail("configuration must be a plain JSON object")
	}
	providers := map[string]json.RawMessage{}
	if raw, ok := settings["provider"]; ok {
		if json.Unmarshal(raw, &providers) != nil || providers == nil {
			return fail("provider must be a JSON object")
		}
	}
	var owned openCodeProxyOwnership
	if raw, ok := providers["cliproxyapi"]; ok {
		var provider struct {
			Options map[string]json.RawMessage `json:"options"`
		}
		if json.Unmarshal(raw, &provider) != nil || json.Unmarshal(provider.Options[proxyMetadata], &owned) != nil || owned.Version != 1 {
			return fail("cliproxyapi provider is not owned by this workflow")
		}
	}
	if options["remove"] != "" {
		if owned.Version == 0 {
			return nil
		}
		delete(providers, "cliproxyapi")
		var model string
		if json.Unmarshal(settings["model"], &model) == nil && strings.HasPrefix(model, "cliproxyapi/") {
			if len(owned.PreviousModel) > 0 {
				settings["model"] = owned.PreviousModel
			} else {
				delete(settings, "model")
			}
		}
	} else {
		var model string
		if owned.Version == 0 || json.Unmarshal(settings["model"], &model) != nil || !strings.HasPrefix(model, "cliproxyapi/") {
			owned.PreviousModel = settings["model"]
		}
		owned.Version = 1
		provider := map[string]any{
			"npm":     "@ai-sdk/openai-compatible",
			"name":    "CLIProxyAPI",
			"options": map[string]any{"baseURL": options["url"] + "/v1", "apiKey": key, proxyMetadata: owned},
			"models":  map[string]any{options["model"]: map[string]any{"name": options["model"]}},
		}
		providers["cliproxyapi"], _ = json.Marshal(provider)
		settings["model"], _ = json.Marshal("cliproxyapi/" + options["model"])
	}
	settings["provider"], _ = json.Marshal(providers)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fail("cannot prepare configuration directory")
	}
	if err := writeJSONFile(path, ".proxy-", settings); err != nil {
		return fail("cannot atomically write configuration")
	}
	return nil
}
