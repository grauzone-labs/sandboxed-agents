package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/image"
	"github.com/grauzone-labs/sandboxed-agents/internal/capture"
)

// rebuildToolchains rebuilds sorted sets with bounded workers and publishes complete, disk-spooled output blocks in completion order.
func rebuildToolchains(p podmanTarget, sets map[string][]string, names []string, hash, baseID string, limit int, output, progress io.Writer) []error {
	type result struct {
		index int
		tag   string
		log   *os.File
		err   error
	}
	jobs := make(chan int)
	done := make(chan result)
	streams := &serializedBuildWriter{writer: progress}
	progress = streams
	for range min(limit, len(names)) {
		go func() {
			for i := range jobs {
				log, err := os.CreateTemp("", "sandboxed-agents-build-output-*")
				tag := ""
				if err == nil {
					tag, err = rebuildSpooledImage(p, sets[names[i]], hash, baseID, log, progress)
				} else {
					err = fmt.Errorf("create build output spool: %w", err)
				}
				done <- result{i, tag, log, err}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for i := range names {
			jobs <- i
		}
	}()
	failures := make([]error, len(names))
	for range names {
		result := <-done
		// A complete stdout block also excludes progress writes when both streams share a terminal or writer.
		streams.mu.Lock()
		err := result.err
		if result.log != nil {
			err = errors.Join(err, publishBuildLog(output, "toolchain set "+names[result.index], result.log))
		}
		if err == nil {
			_, err = fmt.Fprintln(output, "built image", result.tag)
		}
		streams.mu.Unlock()
		failures[result.index] = err
	}
	return failures
}

// rebuildSpooledImage holds one tag through build and primary evidence publication, collecting both streams with actual-byte progress.
func rebuildSpooledImage(p podmanTarget, set []string, hash, baseID string, log *os.File, progress io.Writer) (string, error) {
	lock, err := acquireImageTagLock(p, image.Tag(hash, set), progress)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	return buildSpooledImageUnderLock(p, set, hash, baseID, log, progress)
}

// rebuildBaseWithProgress rebuilds the base under its caller's tag lock and publishes its complete output with live byte counts.
func rebuildBaseWithProgress(p podmanTarget, hash string, output, progress io.Writer) (string, error) {
	log, err := os.CreateTemp("", "sandboxed-agents-build-output-*")
	if err != nil {
		return "", fmt.Errorf("create build output spool: %w", err)
	}
	tag, err := buildSpooledImageUnderLock(p, nil, hash, "", log, progress)
	return tag, errors.Join(err, publishBuildLog(output, "base image", log))
}

// publishBuildLog emits one complete output block and reports any publication or spool cleanup failure.
func publishBuildLog(output io.Writer, name string, log *os.File) error {
	_, headingErr := fmt.Fprintln(output, "build output for "+name+":")
	_, seekErr := log.Seek(0, io.SeekStart)
	var copyErr error
	if seekErr == nil {
		_, copyErr = io.Copy(output, log)
	}
	_, newlineErr := fmt.Fprintln(output)
	closeErr := log.Close()
	removeErr := os.Remove(log.Name())
	if err := errors.Join(headingErr, seekErr, copyErr, newlineErr, closeErr, removeErr); err != nil {
		return fmt.Errorf("publish build output: %w", err)
	}
	return nil
}

// buildSpooledImageUnderLock applies the common build protocol through overlapped-capable capture with complete output spooling.
func buildSpooledImageUnderLock(p podmanTarget, set []string, hash, baseID string, log *os.File, progress io.Writer) (string, error) {
	return buildLabeledImageWithRunner(p, set, hash, baseID, true, func(args ...string) error {
		writer := &buildProgressWriter{log: log, progress: progress, name: toolchainSet(set)}
		command := p.command(context.Background(), 0, args...)
		if err := capture.Run(command, writer, writer); err != nil {
			// Original process output is retained in the image's block, without constructing a second unbounded diagnostic string.
			return fmt.Errorf("podman %s: %w", strings.Join(command.Args[1:], " "), err)
		}
		return nil
	})
}

// serializedBuildWriter serializes progress and lock notices with completed output blocks, including when the caller shares one writer for both streams.
type serializedBuildWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

// Write publishes one build-progress write while excluding other workers.
func (w *serializedBuildWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(data)
}

// buildProgressWriter spools complete process output and reports the first bytes and subsequent changes at most once per second.
type buildProgressWriter struct {
	mu       sync.Mutex
	log      io.Writer
	progress io.Writer
	name     string
	bytes    int64
	last     time.Time
}

// Write retains every byte and publishes per-image byte counts while the build is running.
func (w *buildProgressWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.log.Write(data)
	w.bytes += int64(n)
	now := time.Now()
	if n > 0 && (w.last.IsZero() || now.Sub(w.last) >= time.Second) {
		w.last = now
		// Progress is supplemental; a failed status write does not retry or change a build.
		fmt.Fprintf(w.progress, "build progress %s: %d bytes received\n", w.name, w.bytes)
	}
	return n, err
}
