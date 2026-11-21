package liverun_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
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

// inventoryWriter emulates another writer that uses engine space while preparation reads the container inventory.
type inventoryWriter struct {
	liverun.Executor
	consume func()
}

// Execute forwards each query and consumes space when the container listing runs.
func (w inventoryWriter) Execute(ctx context.Context, in liverun.Invocation) ([]byte, error) {
	if strings.Contains(strings.Join(in.Args, " "), "ps --all") {
		w.consume()
	}
	return w.Executor.Execute(ctx, in)
}

// TestPrepareChecksHeadroomAfterInventory proves the prepare command seam compares the 100-byte known peak with free
// space measured after the inventory, so space another writer used during the inventory refuses the run.
func TestPrepareChecksHeadroomAfterInventory(t *testing.T) {
	svc, fake, args, _ := commandHarness(t)
	free := uint64(900)
	svc.Executor = inventoryWriter{Executor: fake, consume: func() { free = 50 }}
	svc.FreeSpace = func(string) (uint64, error) { return free, nil }
	var out bytes.Buffer
	err := svc.RunDispatch(context.Background(), args, &out, &out)
	if err == nil || !strings.Contains(err.Error(), "headroom") || !strings.Contains(err.Error(), "engine 50 bytes free") {
		t.Fatalf("stale free space admitted the run: %v %s", err, &out)
	}
}
