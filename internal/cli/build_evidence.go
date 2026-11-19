package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
	"os"
	"strings"
	"time"
)

// withBuildEvidence captures primary final IDs and registry FROM inputs under the image-tag lock with the opted-in single-commit policy.
// Failed confirmation records an unconfirmed observation, or an identity-free channel-local marker if its manifest cannot be validated.
// Telemetry remains supplemental; stdout and RUN output never authorize publication or removal.
func withBuildEvidence(p podmanTarget, tag, hash string, args []string, build func(string, string) error) error {
	directory := os.Getenv(liverun.BuildEvidenceEnv)
	if directory == "" {
		return build("", "")
	}
	if confirmBuildTarget(p, directory) != nil {
		_ = liverun.RecordUnconfirmedBuild(directory)
		return build("", "")
	}
	inspect := func(name string) (string, error) {
		record, err := inspectBuildTarget(p, directory, name)
		if err != nil {
			return "", err
		}
		if record.Labels[assetHashLabel] != hash {
			return "", fmt.Errorf("build result asset unverified")
		}
		return record.ID, nil
	}
	return liverun.CaptureSingleCommitBuild(directory, tag, hash, reproducibleBuild(p.host.Platform, p.environment(), args), func(iid string) error {
		buildErr := build(iid, "--layers=false")
		recordRegistryFrom(p, directory, args)
		return buildErr
	}, inspect)
}

// inspectBuildTarget confirms the invocation engine before a bounded primary inspection yielding exactly one image.
func inspectBuildTarget(p podmanTarget, directory, name string) (imageRecord, error) {
	if err := confirmBuildTarget(p, directory); err != nil {
		return imageRecord{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := p.queryContext(ctx, time.Second, "image", "inspect", name)
	if err != nil {
		return imageRecord{}, err
	}
	var records []imageRecord
	if json.Unmarshal([]byte(output), &records) != nil || len(records) != 1 {
		return imageRecord{}, fmt.Errorf("build result inspection unverified")
	}
	return records[0], nil
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

// reproducibleBuild detects explicit config-timestamp controls without using time as ownership evidence.
func reproducibleBuild(platform string, environment, args []string) bool {
	for _, value := range environment {
		key, _, present := strings.Cut(value, "=")
		if present && (key == "SOURCE_DATE_EPOCH" || platform == "windows" && strings.EqualFold(key, "SOURCE_DATE_EPOCH")) {
			return true
		}
	}
	for _, arg := range args {
		for _, flag := range []string{"--timestamp", "--source-date-epoch", "--rewrite-timestamp", "--omit-timestamp"} {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				return true
			}
		}
	}
	return false
}

// recordRegistryFrom retains only an inspected literal registry FROM identity from the actual materialized recipe.
// It excludes product-labelled images and never attributes a pull or grants deletion authority.
func recordRegistryFrom(p podmanTarget, directory string, args []string) {
	reference := literalRegistryFrom(args)
	if reference == "" {
		return
	}
	image, err := inspectBuildTarget(p, directory, reference)
	if err != nil {
		return
	}
	if _, present := image.Labels[assetHashLabel]; present {
		return
	}
	if _, present := image.Labels[imageMarkerLabel]; present {
		return
	}
	_ = liverun.RecordRegistryBuildInput(directory, reference, image.ID)
}

// literalRegistryFrom reads one bounded single-stage recipe; unresolved/unsupported references provide no exclusion.
func literalRegistryFrom(args []string) string {
	path := ""
	for i, arg := range args {
		if arg == "--file" && i+1 < len(args) {
			path = args[i+1]
			break
		}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	reference := ""
	stages := 0
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && strings.EqualFold(fields[0], "FROM") {
			stages++
			if len(fields) == 2 {
				reference = fields[1]
			}
		}
	}
	if stages != 1 || reference == "" || strings.ContainsAny(reference, "${}") || strings.HasPrefix(reference, "localhost/") {
		return ""
	}
	return reference
}
