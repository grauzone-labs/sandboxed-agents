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
func withBuildEvidence(p podmanTarget, tag, hash string, build func(string) error) error {
	return liverun.CaptureBuildResult(os.Getenv(liverun.BuildEvidenceEnv), tag, hash, build, func(name string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		info, err := p.queryContext(ctx, time.Second, "info", "--format", "json")
		if err != nil {
			return "", err
		}
		var engine struct {
			Host  struct{ Hostname string }
			Store struct{ GraphRoot string }
		}
		if json.Unmarshal([]byte(info), &engine) != nil || engine.Host.Hostname == "" || engine.Store.GraphRoot == "" {
			return "", fmt.Errorf("build engine identity unverified")
		}
		if err = liverun.ConfirmBuildEngine(os.Getenv(liverun.BuildEvidenceEnv), engine.Host.Hostname+":"+engine.Store.GraphRoot); err != nil {
			return "", err
		}
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
