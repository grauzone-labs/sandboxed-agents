package cli

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestBuildProgressUsesActualBytesAndClock proves first progress, the one-second rate bound, and silence without new bytes at the module's output-writer/clock seam.
func TestBuildProgressUsesActualBytesAndClock(t *testing.T) {
	var log, progress bytes.Buffer
	now := time.Unix(100, 0)
	writer := &buildProgressWriter{log: &log, progress: &progress, name: "azure", now: func() time.Time { return now }}
	if progress.Len() != 0 {
		t.Fatal("progress appeared before output")
	}
	for _, test := range []struct {
		at         time.Duration
		data, want string
	}{
		{0, "", ""},
		{0, "ab", "build progress azure: 2 bytes received\n"},
		{999 * time.Millisecond, "cd", "build progress azure: 2 bytes received\n"},
		{time.Second, "e", "build progress azure: 2 bytes received\nbuild progress azure: 5 bytes received\n"},
		{6 * time.Second, "", "build progress azure: 2 bytes received\nbuild progress azure: 5 bytes received\n"},
		{6 * time.Second, "f", "build progress azure: 2 bytes received\nbuild progress azure: 5 bytes received\nbuild progress azure: 6 bytes received\n"},
	} {
		now = time.Unix(100, 0).Add(test.at)
		n, err := writer.Write([]byte(test.data))
		if err != nil || n != len(test.data) || progress.String() != test.want {
			t.Fatalf("at=%s input=%q n=%d err=%v progress=%q want=%q", test.at, test.data, n, err, progress.String(), test.want)
		}
	}
	if log.String() != "abcdef" {
		t.Fatalf("retained log=%q", log.String())
	}
}

// buildPublicationObserver observes the start of a block without exposing bytes.Buffer's optimized copy methods.
type buildPublicationObserver struct {
	buffer  bytes.Buffer
	observe func()
}

// Write observes the first publication write and retains every subsequent byte.
func (w *buildPublicationObserver) Write(data []byte) (int, error) {
	if w.observe != nil {
		observe := w.observe
		w.observe = nil
		observe()
	}
	return w.buffer.Write(data)
}

// TestBuildPublicationExcludesAliasedProgressAndLockNotices proves whole-block publication owns exclusion while worker notices share its caller writer at the module's publisher seam.
func TestBuildPublicationExcludesAliasedProgressAndLockNotices(t *testing.T) {
	output := &buildPublicationObserver{}
	publisher := &serializedBuildWriter{writer: output}
	log, err := newBuildOutputSpool()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.Write([]byte("original engine output\n")); err != nil {
		t.Fatal(err)
	}
	var writers sync.WaitGroup
	output.observe = func() {
		// This module-owned invariant check makes the exclusion assertion independent of goroutine scheduling.
		if publisher.mu.TryLock() {
			publisher.mu.Unlock()
			t.Error("block callback ran without the publisher's exclusion")
		}
		for _, notice := range []string{"build progress dotnet: 8 bytes received\n", "waiting for image-tag lock: private-lock\n"} {
			ready := make(chan struct{})
			writers.Add(1)
			go func(text string) { defer writers.Done(); close(ready); fmt.Fprint(publisher, text) }(notice)
			<-ready
		}
	}
	outcome := publishRebuildOutcome(publisher, output, "toolchain set azure", imageBuildOutcome{tag: "test-tag", log: log})
	writers.Wait()
	if outcome.buildErr != nil || outcome.outputErr != nil {
		t.Fatalf("outcome=%+v", outcome)
	}
	const block = "build output for toolchain set azure:\noriginal engine output\n\nbuilt image test-tag\n"
	if !strings.HasPrefix(output.buffer.String(), block) {
		t.Fatalf("notice entered complete block: %q", output.buffer.String())
	}
	rest := strings.TrimPrefix(output.buffer.String(), block)
	for _, notice := range []string{"build progress dotnet: 8 bytes received\n", "waiting for image-tag lock: private-lock\n"} {
		if strings.Count(rest, notice) != 1 {
			t.Fatalf("lost notice %q in %q", notice, rest)
		}
	}
}
