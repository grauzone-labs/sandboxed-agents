package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/azurebrowser"
	"github.com/grauzone-labs/sandboxed-agents/internal/capture"
)

// azureTunnelReady is emitted by the remote command after SSH establishes forwarding.
const azureTunnelReady = "sandboxed-agents-azure-ready\n"

// azureOutput serializes terminal output and consumes bounded private callback records.
type azureOutput struct {
	mu       sync.Mutex
	output   io.Writer
	pending  []byte
	callback func(string) error
	cancel   context.CancelFunc
	err      error
}

// Write forwards ordinary terminal bytes immediately and handles complete callback records.
func (w *azureOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	defer func() {
		if w.err != nil {
			w.cancel()
		}
	}()
	if w.err != nil {
		return 0, w.err
	}
	size := len(data)
	w.pending = append(w.pending, data...)
	for len(w.pending) > 0 {
		start := bytes.IndexByte(w.pending, '\x1e')
		if start < 0 {
			_, w.err = w.output.Write(w.pending)
			w.pending = nil
			break
		}
		if start > 0 {
			if _, w.err = w.output.Write(w.pending[:start]); w.err != nil {
				break
			}
			w.pending = w.pending[start:]
		}
		prefix := []byte(azurebrowser.Prefix)
		if len(w.pending) < len(prefix) && callbackPrefix(w.pending) {
			break
		}
		if !bytes.HasPrefix(w.pending, prefix) {
			_, w.err = w.output.Write(w.pending[:1])
			w.pending = w.pending[1:]
			if w.err != nil {
				break
			}
			continue
		}
		end := bytes.IndexByte(w.pending, '\n')
		if end > 65536 || (end < 0 && len(w.pending) > 65536) {
			w.err = fmt.Errorf("Azure browser callback exceeds size limit")
			break
		}
		if end < 0 {
			break
		}
		var address string
		if err := json.Unmarshal(w.pending[len(prefix):end], &address); err != nil {
			w.err = fmt.Errorf("invalid Azure browser callback record")
			break
		}
		if w.err = w.callback(address); w.err != nil {
			break
		}
		w.pending = w.pending[end+1:]
	}
	if w.err != nil {
		return 0, w.err
	}
	return size, nil
}

// azureTunnel owns one SSH forwarding process until login ends.
type azureTunnel struct {
	cancel   context.CancelFunc
	done     chan struct{}
	stdin    io.WriteCloser
	stopping chan struct{}
}

// close kills and reaps the tunnel, closing its input so the remote readiness command also exits.
func (t *azureTunnel) close() {
	close(t.stopping)
	t.stdin.Close()
	t.cancel()
	<-t.done
}

// startAzureTunnel waits for a remote marker emitted only after SSH establishes local forwarding.
func startAzureTunnel(ctx context.Context, alias string, port int) (*azureTunnel, error) {
	tunnelCtx, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(tunnelCtx, "ssh", "-T", "-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", port, port),
		"-o", "ExitOnForwardFailure=yes", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "PermitLocalCommand=no", alias,
		"printf '"+strings.ReplaceAll(azureTunnelReady, "\n", `\n`)+"'; cat >/dev/null")
	command.WaitDelay = 2 * time.Second
	childInput, stdin, err := capture.InputPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	reader, writer := io.Pipe()
	command.Stdin = childInput
	// Diagnostics from the sandbox or SSH may include authentication data; return a bounded generic error.
	wait, err := capture.Start(command, writer, io.Discard)
	childInput.Close()
	if err != nil {
		stdin.Close()
		reader.Close()
		writer.Close()
		cancel()
		return nil, err
	}
	tunnel := &azureTunnel{cancel: cancel, done: make(chan struct{}), stdin: stdin, stopping: make(chan struct{})}
	go func() { wait(); writer.Close(); close(tunnel.done) }()
	ready := make(chan bool, 1)
	go func() {
		marker := make([]byte, len(azureTunnelReady))
		_, err := io.ReadFull(reader, marker)
		ready <- err == nil && string(marker) == azureTunnelReady
		reader.Close()
	}()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case ok := <-ready:
		if ok {
			return tunnel, nil
		}
	case <-tunnel.done:
	case <-ctx.Done():
	case <-timer.C:
	}
	reader.Close()
	tunnel.close()
	return nil, fmt.Errorf("Azure browser SSH tunnel could not establish forwarding; check SSH setup and callback port, or use device-code")
}

// runAzureBrowser streams login while opening each requested tunnel before exposing its URL.
func runAzureBrowser(request Request, p podmanTarget, stdin io.Reader, stdout, stderr io.Writer, args []string) (int, error) {
	restore := snapshotAzureTerminal(stdin, stdout, stderr)
	defer restore()
	ctx, cancel := azureLoginContext()
	defer cancel()
	var tunnel *azureTunnel
	defer func() {
		if tunnel != nil {
			tunnel.close()
		}
	}()
	output := &azureOutput{output: stdout, cancel: cancel}
	output.callback = func(address string) error {
		port, err := azurebrowser.Port(address)
		if err != nil {
			cancel()
			return err
		}
		if tunnel != nil {
			tunnel.close()
		}
		tunnel, err = startAzureTunnel(ctx, sshHostName(request), port)
		if err != nil {
			cancel()
			return err
		}
		current := tunnel
		go func() {
			<-current.done
			select {
			case <-current.stopping:
			default:
				cancel()
			}
		}()
		_, err = fmt.Fprintf(stdout, "SSH tunnel ready. Open this URL in a browser on this machine:\r\n%s\r\n", address)
		if err != nil {
			cancel()
		}
		return err
	}
	command := p.command(ctx, 2*time.Second, args...)
	command.Stdin = stdin
	err := capture.Run(command, output, azureStderr{output: output, writer: stderr})
	if output.err != nil {
		return 1, output.err
	}
	if len(output.pending) > 0 {
		if callbackPrefix(output.pending) {
			return 1, fmt.Errorf("incomplete Azure browser callback record")
		}
		if _, writeErr := stdout.Write(output.pending); writeErr != nil {
			return 1, writeErr
		}
	}
	if tunnel != nil {
		select {
		case <-tunnel.done:
			return 1, fmt.Errorf("Azure browser SSH tunnel ended before login completed; retry login or use device-code")
		default:
		}
	}
	if ctx.Err() != nil {
		return 1, fmt.Errorf("Azure browser login cancelled")
	}
	if err != nil {
		return 1, fmt.Errorf("integration workflow failed")
	}
	return 0, nil
}

// azureStderr preserves diagnostics on stderr while sharing the stdout writer's serialization.
type azureStderr struct {
	output *azureOutput
	writer io.Writer
}

// Write serializes stderr with callback processing without interpreting diagnostics as records.
func (w azureStderr) Write(data []byte) (int, error) {
	w.output.mu.Lock()
	defer w.output.mu.Unlock()
	n, err := w.writer.Write(data)
	if err != nil {
		w.output.cancel()
	}
	return n, err
}

// callbackPrefix recognizes either a complete private record prefix or its incomplete start.
func callbackPrefix(data []byte) bool {
	prefix := []byte(azurebrowser.Prefix)
	return bytes.HasPrefix(data, prefix) || bytes.HasPrefix(prefix, data)
}
