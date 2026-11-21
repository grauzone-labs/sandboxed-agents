package liverun

import (
	"bytes"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

// TestCleanupEvidenceTextBoundsOversizedOutput proves the retained query output
// of an oversized, escape-heavy answer fits its encoded budget with the
// truncation marker, and is cut to the budget before it is copied or encoded,
// so the work stays near the budget rather than the answer's size. It
// exercises the cleanup query evidence seam.
func TestCleanupEvidenceTextBoundsOversizedOutput(t *testing.T) {
	const budget = 1024
	answer := bytes.Repeat([]byte{1}, 4<<20)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	text := cleanupEvidenceText(answer, budget)
	runtime.ReadMemStats(&after)
	encoded, err := json.Marshal(text)
	if err != nil || len(encoded) > budget || !strings.HasSuffix(text, " [truncated]") || !strings.HasPrefix(text, "\x01") {
		t.Fatalf("retained %d encoded bytes (%v), marker kept=%v", len(encoded), err, strings.HasSuffix(text, " [truncated]"))
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 256<<10 {
		t.Fatalf("retaining %d budget bytes allocated %d bytes for a %d-byte answer", budget, allocated, len(answer))
	}
	if short := cleanupEvidenceText([]byte("short answer"), budget); short != "short answer" {
		t.Fatalf("an answer within budget was changed: %q", short)
	}
}
