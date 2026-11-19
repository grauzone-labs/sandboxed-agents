package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
	"os"
	"strconv"
	"strings"
	"time"
)

// withBuildEvidence captures primary final IDs and optional per-attempt audit metadata under the image-tag lock.
// Telemetry remains supplemental; stdout and RUN output never authorize publication or removal.
func withBuildEvidence(p podmanTarget, tag, hash string, build func(string, string) error) error {
	directory := os.Getenv(liverun.BuildEvidenceEnv)
	if directory != "" && confirmBuildTarget(p, directory) != nil {
		return build("", "")
	}
	inspect := func(name string) (string, error) {
		if err := confirmBuildTarget(p, directory); err != nil {
			return "", err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		output, err := p.queryContext(ctx, time.Second, "image", "inspect", name)
		if err != nil {
			return "", err
		}
		record, err := decodeBuildInspection(output, hash)
		return record, err
	}
	if directory != "" && layerEvidenceSupported(p) {
		return liverun.CaptureLayerBuildResult(directory, tag, hash, build, inspect)
	}
	return liverun.CaptureBuildResult(directory, tag, hash, func(iid string) error { return build(iid, "") }, inspect)
}

// decodeBuildInspection verifies the primary final ID and expected asset label on the inspected tag.
func decodeBuildInspection(output, hash string) (string, error) {
	var records []imageRecord
	if json.Unmarshal([]byte(output), &records) != nil || len(records) != 1 || records[0].Labels[assetHashLabel] != hash {
		return "", fmt.Errorf("build result inspection unverified")
	}
	return records[0].ID, nil
}

// confirmBuildTarget binds builder-controlled metadata to the actual selected primary engine query.
func confirmBuildTarget(p podmanTarget, directory string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	info, err := p.queryContext(ctx, time.Second, "info", "--format", "json")
	if err != nil {
		return err
	}
	identity, _, err := liverun.EngineIdentity([]byte(info))
	if err != nil {
		return err
	}
	return liverun.ConfirmBuildEngine(directory, identity)
}

// layerEvidenceSupported probes the trusted client help and both version sides before optional layer metadata is requested.
// Versioned Podman 4.7 sources implement the flag and remote layerLabel parameter; older/unknown sides retain iid-only behavior.
func layerEvidenceSupported(p podmanTarget) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	data, err := p.queryContext(ctx, time.Second, "version", "--format", "json")
	var version struct {
		Client *struct{ Version string }
		Server *struct{ Version string }
	}
	if err != nil || json.Unmarshal([]byte(data), &version) != nil || version.Client == nil || !layerVersionKnown(version.Client.Version) {
		return false
	}
	if version.Server != nil {
		if !layerVersionKnown(version.Server.Version) {
			return false
		}
	} else if p.connection != "" || p.host.Platform == "windows" {
		return false
	}
	help, err := p.queryContext(ctx, time.Second, "build", "--help")
	if err != nil {
		return false
	}
	for _, word := range strings.Fields(help) {
		if word == "--layer-label" || strings.HasPrefix(word, "--layer-label=") {
			return true
		}
	}
	return false
}

// layerVersionKnown admits only documented release versions at or after known client/server layer-label support.
func layerVersionKnown(version string) bool {
	if strings.ContainsAny(version, "-+") {
		return false
	}
	fields := strings.Split(version, ".")
	if len(fields) != 3 {
		return false
	}
	values := make([]int, 3)
	for i, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil || n < 0 {
			return false
		}
		values[i] = n
	}
	return values[0] > 4 || values[0] == 4 && values[1] >= 7
}
