package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
	"os"
	"time"
)

// withBuildEvidence captures only primary final identities while the caller holds the image-tag lock.
// Telemetry remains supplemental; stdout and RUN output never authorize publication or removal.
func withBuildEvidence(p podmanTarget, tag, hash string, build func(string, string) error) error {
	directory := os.Getenv(liverun.BuildEvidenceEnv)
	if directory != "" && confirmBuildTarget(p, directory) != nil {
		return build("", "")
	}
	return liverun.CaptureLayerBuildResult(directory, tag, hash, build, func(name string) (string, error) {
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
	})
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
