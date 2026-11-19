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

// imageBuildOutcome keeps an unsuccessful rebuild distinct from output errors after a tag was rebuilt.
type imageBuildOutcome struct {
	tag       string
	buildErr  error
	outputErr error
	log       *buildOutputSpool
}

// imageRebuildContext carries one selected target, asset hash, and captured base ID unchanged through a rebuild phase.
// Value receivers keep concurrent workers on the same immutable build inputs; image sets and output sinks vary per operation.
type imageRebuildContext struct {
	target podmanTarget
	hash   string
	baseID string
}

// rebuildToolchains rebuilds sorted sets with bounded workers and publishes complete blocks without treating output failures as failed sets.
func (b imageRebuildContext) rebuildToolchains(sets map[string][]string, names []string, limit int, output, progress io.Writer) ([]error, error) {
	type result struct {
		index   int
		outcome imageBuildOutcome
	}
	jobs := make(chan int)
	done := make(chan result)
	publisher := &serializedBuildWriter{writer: progress}
	for range min(limit, len(names)) {
		go func() {
			for i := range jobs {
				done <- result{i, b.rebuildSpooledImage(sets[names[i]], publisher)}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for i := range names {
			jobs <- i
		}
	}()
	buildFailures := make([]error, len(names))
	outputErrors := make([]error, len(names))
	for range names {
		result := <-done
		outcome := publishRebuildOutcome(publisher, output, "toolchain set "+names[result.index], result.outcome)
		buildFailures[result.index], outputErrors[result.index] = outcome.buildErr, outcome.outputErr
	}
	return buildFailures, errors.Join(outputErrors...)
}

// rebuildSpooledImage holds one tag through build and primary evidence publication, releasing it before output publication.
func (b imageRebuildContext) rebuildSpooledImage(set []string, progress io.Writer) imageBuildOutcome {
	lock, err := acquireImageTagLock(b.target, image.Tag(b.hash, set), progress)
	if err != nil {
		return imageBuildOutcome{tag: image.Tag(b.hash, set), buildErr: err}
	}
	defer lock.Close()
	return b.buildSpooledImageUnderLock(set, progress)
}

// rebuildBaseWithProgress publishes a rebuilt base's output separately from its build result so toolchain work can continue after output errors.
func (b imageRebuildContext) rebuildBaseWithProgress(output, progress io.Writer) imageBuildOutcome {
	publisher := &serializedBuildWriter{writer: progress}
	outcome := b.buildSpooledImageUnderLock(nil, publisher)
	return publishRebuildOutcome(publisher, output, "base image", outcome)
}

// publishRebuildOutcome publishes one complete image block and its successful result under the publisher's exclusion, retaining accurate tag outcomes on I/O errors.
func publishRebuildOutcome(publisher *serializedBuildWriter, output io.Writer, name string, outcome imageBuildOutcome) imageBuildOutcome {
	err := publisher.exclusive(func() error {
		var logErr, resultErr error
		if outcome.log != nil {
			logErr = outcome.log.publish(output, name)
		}
		if outcome.buildErr == nil {
			_, resultErr = fmt.Fprintln(output, "built image", outcome.tag)
		}
		if resultErr != nil {
			resultErr = fmt.Errorf("publish build result: %w", resultErr)
		}
		return errors.Join(logErr, resultErr)
	})
	outputErr := errors.Join(outcome.outputErr, err)
	if outputErr != nil {
		if outcome.buildErr == nil {
			outputErr = fmt.Errorf("image %s was rebuilt; %w", outcome.tag, outputErr)
		} else {
			outputErr = fmt.Errorf("image %s build output: %w", outcome.tag, outputErr)
		}
	}
	outcome.outputErr = outputErr
	return outcome
}

// buildOutputSpool owns the creation, complete output, publication, and exact-path cleanup of one private temporary log.
type buildOutputSpool struct{ file *os.File }

// newBuildOutputSpool creates one private log whose normal completion cleanup belongs to its owner.
func newBuildOutputSpool() (*buildOutputSpool, error) {
	file, err := os.CreateTemp("", "sandboxed-agents-build-output-*")
	if err != nil {
		return nil, fmt.Errorf("create build output spool: %w", err)
	}
	return &buildOutputSpool{file: file}, nil
}

// Write retains process bytes in the owned log without truncation.
func (s *buildOutputSpool) Write(data []byte) (int, error) { return s.file.Write(data) }

// publish emits the complete log and closes and removes only its own path, reporting each failed operation accurately.
func (s *buildOutputSpool) publish(output io.Writer, name string) error {
	_, publicationErr := fmt.Fprintln(output, "build output for "+name+":")
	if publicationErr == nil {
		if _, publicationErr = s.file.Seek(0, io.SeekStart); publicationErr == nil {
			_, publicationErr = io.Copy(output, s.file)
		}
		if publicationErr == nil {
			_, publicationErr = fmt.Fprintln(output)
		}
	}
	closeErr := s.file.Close()
	if closeErr != nil {
		closeErr = fmt.Errorf("close build output spool: %w", closeErr)
	}
	removeErr := os.Remove(s.file.Name())
	if removeErr != nil {
		removeErr = fmt.Errorf("remove build output spool: %w", removeErr)
	}
	if err := errors.Join(publicationErr, closeErr, removeErr); err != nil {
		return fmt.Errorf("publish build output for %s: %w", name, err)
	}
	return nil
}

// buildSpooledImageUnderLock uses the common build/evidence protocol and separates capture or context cleanup errors after a successful engine exit.
func (b imageRebuildContext) buildSpooledImageUnderLock(set []string, progress io.Writer) imageBuildOutcome {
	outcome := imageBuildOutcome{tag: image.Tag(b.hash, set)}
	log, err := newBuildOutputSpool()
	if err != nil {
		outcome.buildErr = err
		return outcome
	}
	outcome.log = log
	rebuilt := false
	_, err = buildLabeledImageWithRunner(b.target, set, b.hash, b.baseID, true, func(args ...string) error {
		writer := &buildProgressWriter{log: log, progress: progress, name: toolchainSet(set)}
		command := b.target.command(context.Background(), 0, args...)
		captureErr := capture.Run(command, writer, writer)
		rebuilt = command.ProcessState != nil && command.ProcessState.Success()
		if captureErr != nil {
			if rebuilt {
				outcome.outputErr = fmt.Errorf("capture build output: %w", captureErr)
				return nil
			}
			return fmt.Errorf("podman %s: %w", strings.Join(command.Args[1:], " "), captureErr)
		}
		return nil
	})
	if rebuilt {
		if err != nil {
			outcome.outputErr = errors.Join(outcome.outputErr, fmt.Errorf("finish rebuilt image: %w", err))
		}
	} else {
		outcome.buildErr = err
	}
	return outcome
}

// serializedBuildWriter owns exclusion between progress/lock notices and complete output blocks, including aliased caller streams.
type serializedBuildWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

// exclusive publishes one complete block while excluding progress and lock notices.
func (w *serializedBuildWriter) exclusive(publish func() error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return publish()
}

// Write publishes one progress or lock notice while excluding complete blocks and other workers.
func (w *serializedBuildWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(data)
}

// buildProgressWriter retains all process output and reports first bytes and subsequent changes at most once per second.
type buildProgressWriter struct {
	mu       sync.Mutex
	log      io.Writer
	progress io.Writer
	name     string
	bytes    int64
	last     time.Time
	now      func() time.Time
}

// Write retains every byte and publishes per-image byte counts on actual output, with an optional internal clock seam.
func (w *buildProgressWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.log.Write(data)
	w.bytes += int64(n)
	now := time.Now()
	if w.now != nil {
		now = w.now()
	}
	if n > 0 && (w.last.IsZero() || now.Sub(w.last) >= time.Second) {
		w.last = now
		// Supplemental progress failure never retries or changes an executed build.
		fmt.Fprintf(w.progress, "build progress %s: %d bytes received\n", w.name, w.bytes)
	}
	return n, err
}
