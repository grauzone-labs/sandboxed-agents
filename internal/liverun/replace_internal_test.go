package liverun

import (
	"errors"
	"testing"
	"time"
)

// errBusy stands for a replacement blocked by a reader's open handle.
var errBusy = errors.New("destination open without delete sharing")

// TestRetryReplaceOutlastsBriefReaders proves artifact publication succeeds once
// a reader's handle closes within the retry window, returns other failures at
// once, and gives up on a destination that stays busy. It exercises the
// artifact replacement retry seam.
func TestRetryReplaceOutlastsBriefReaders(t *testing.T) {
	busy := func(err error) bool { return errors.Is(err, errBusy) }
	calls := 0
	err := retryReplace(func() error {
		calls++
		if calls < 3 {
			return errBusy
		}
		return nil
	}, busy, time.Second)
	if err != nil || calls != 3 {
		t.Fatalf("brief reader blocked publication: calls=%d err=%v", calls, err)
	}
	calls = 0
	other := errors.New("not a directory")
	if err = retryReplace(func() error { calls++; return other }, busy, time.Second); !errors.Is(err, other) || calls != 1 {
		t.Fatalf("persistent failure retried: calls=%d err=%v", calls, err)
	}
	started := time.Now()
	if err = retryReplace(func() error { return errBusy }, busy, 50*time.Millisecond); !errors.Is(err, errBusy) {
		t.Fatalf("permanently busy destination reported published: %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("busy retry exceeded its window")
	}
}
