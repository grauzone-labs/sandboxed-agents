package liverun_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestPrepareRefusesRemoteLinuxEngine proves the prepare command seam refuses a Linux Podman client that reports a
// remote service before any storage measurement, so headroom never comes from a local path the remote engine named.
func TestPrepareRefusesRemoteLinuxEngine(t *testing.T) {
	svc, fake, args, _ := commandHarness(t)
	fake.answers = map[string]string{"info --format json": `{"host":{"hostname":"engine","serviceIsRemote":true},"store":{"graphRoot":"/private/storage"}}`}
	measured := false
	svc.FreeSpace = func(string) (uint64, error) { measured = true; return 900, nil }
	var out bytes.Buffer
	err := svc.RunDispatch(context.Background(), args, &out, &out)
	if err == nil || !strings.Contains(err.Error(), "check engine failed") {
		t.Fatalf("remote engine accepted: %v", err)
	}
	if measured {
		t.Fatal("remote engine storage measured on the local filesystem")
	}
}
