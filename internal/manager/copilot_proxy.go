package manager

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// copilotProxyOwnership identifies the managed provider without copying its plaintext key.
type copilotProxyOwnership struct {
	Version      int    `json:"version"`
	ProviderHash string `json:"providerHash"`
}

// copilotProviderHash hashes canonical provider JSON so field order and escaping do not affect ownership.
func copilotProviderHash(provider map[string]json.RawMessage) string {
	var value any
	data, _ := json.Marshal(provider)
	if json.Unmarshal(data, &value) != nil {
		return ""
	}
	data, _ = json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// writeCopilotProxy merges the managed native provider and model in one private atomic replacement.
func writeCopilotProxy(home string, options map[string]string, key string) error {
	directory := filepath.Join(home, ".copilot")
	path := filepath.Join(directory, "providers.json")
	for _, target := range []string{directory, path} {
		info, err := os.Lstat(target)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !agentFileOwned(info) || (target == directory && !info.IsDir()) || (target == path && !info.Mode().IsRegular()) {
			return errors.New("Copilot registry requires an agent-owned directory and regular unlinked file")
		}
	}
	registry := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return errors.New("cannot read Copilot registry")
	}
	if err == nil && (json.Unmarshal(data, &registry) != nil || registry == nil) {
		return errors.New("malformed Copilot providers.json")
	}
	providers, err := copilotRegistryEntries(registry, "providers", "name")
	if err != nil {
		return err
	}
	models, err := copilotRegistryEntries(registry, "models", "id")
	if err != nil {
		return err
	}
	var owned copilotProxyOwnership
	if raw, exists := registry[proxyMetadata]; exists {
		if json.Unmarshal(raw, &owned) != nil || owned.Version != 1 || len(owned.ProviderHash) != 64 {
			return errors.New("malformed Copilot proxy ownership metadata")
		}
	}
	index := -1
	for i, p := range providers {
		if string(p["name"]) == `"cliproxyapi"` {
			if index >= 0 || owned.Version == 0 || copilotProviderHash(p) != owned.ProviderHash {
				return errors.New("Copilot cliproxyapi provider is not managed or was edited; refusing to overwrite it")
			}
			index = i
		}
	}
	for _, m := range models {
		var provider string
		if json.Unmarshal(m["provider"], &provider) != nil || provider == "" {
			return errors.New("malformed Copilot model provider")
		}
		if provider == "cliproxyapi" && owned.Version == 0 {
			return errors.New("Copilot cliproxyapi model is not managed")
		}
	}
	if options["remove"] != "" {
		if owned.Version == 0 {
			return nil
		}
		if index >= 0 {
			providers = append(providers[:index], providers[index+1:]...)
		}
		kept := models[:0]
		for _, m := range models {
			var provider string
			_ = json.Unmarshal(m["provider"], &provider)
			if provider != "cliproxyapi" {
				kept = append(kept, m)
			}
		}
		models = kept
		delete(registry, proxyMetadata)
	} else {
		p := map[string]json.RawMessage{}
		for name, value := range map[string]string{"name": "cliproxyapi", "type": "openai", "baseUrl": options["url"] + "/v1", "apiKey": key} {
			p[name], _ = json.Marshal(value)
		}
		if options["wire-api"] == "responses" {
			p["wireApi"] = json.RawMessage(`"responses"`)
		}
		if index >= 0 {
			providers[index] = p
		} else {
			providers = append(providers, p)
		}
		found := false
		for _, m := range models {
			var id, provider string
			_ = json.Unmarshal(m["id"], &id)
			_ = json.Unmarshal(m["provider"], &provider)
			if id == options["model"] && provider == "cliproxyapi" {
				found = true
			}
		}
		if !found {
			id, _ := json.Marshal(options["model"])
			models = append(models, map[string]json.RawMessage{"id": id, "provider": json.RawMessage(`"cliproxyapi"`)})
		}
		registry[proxyMetadata], _ = json.Marshal(copilotProxyOwnership{Version: 1, ProviderHash: copilotProviderHash(p)})
	}
	registry["providers"], _ = json.Marshal(providers)
	registry["models"], _ = json.Marshal(models)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return errors.New("cannot prepare Copilot registry directory")
	}
	if err := writeJSONFile(path, ".proxy-", registry); err != nil {
		return errors.New("cannot atomically write Copilot registry")
	}
	return nil
}

// copilotRegistryEntries reads object arrays without discarding unknown fields and validates each entry's identity.
func copilotRegistryEntries(registry map[string]json.RawMessage, field, identity string) ([]map[string]json.RawMessage, error) {
	entries := []map[string]json.RawMessage{}
	if raw, exists := registry[field]; exists {
		if json.Unmarshal(raw, &entries) != nil || entries == nil {
			return nil, fmt.Errorf("malformed Copilot registry %s", field)
		}
	}
	for _, entry := range entries {
		var name string
		if entry == nil || json.Unmarshal(entry[identity], &name) != nil || name == "" {
			return nil, fmt.Errorf("malformed Copilot registry %s entry", field)
		}
		entry[identity], _ = json.Marshal(name)
	}
	return entries, nil
}
