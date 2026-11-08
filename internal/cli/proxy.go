package cli

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// runProxyWorkflow passes a validated key exclusively on manager stdin after sandbox and agent checks.
func runProxyWorkflow(request Request, p podmanTarget, container string, stdout, stderr io.Writer) (int, error) {
	args := []string{"agents", "login", request.Args[1], "proxy"}
	if request.Options["replace-login"] != "" {
		args = append(args, "--replace-login")
	}
	var input io.Reader
	var key string
	if request.Options["remove"] != "" {
		args = append(args, "--remove")
	} else {
		args = append(args, "--url", request.Options["url"])
		if model, ok := request.Options["model"]; ok {
			args = append(args, "--model", model)
		}
		if wire, ok := request.Options["wire-api"]; ok {
			args = append(args, "--wire-api", wire)
		}
		var err error
		if p.host.IsInputTerminal(p.host.Stdin) {
			var data []byte
			data, err = readTerminalSecret(p.host.Stdin, stderr, "Key: ")
			if err == nil {
				key, err = catalog.ReadProxyKey(strings.NewReader(string(data)))
			}
		} else {
			key, err = catalog.ReadProxyKey(p.host.Stdin)
		}
		if err != nil {
			return 1, fmt.Errorf("cannot read a valid proxy key")
		}
		input = strings.NewReader(key + "\n")
		if catalog.ProxyUnencrypted(request.Options["url"]) {
			fmt.Fprintln(stderr, redactProxyOutput("Warning: the proxy key travels unencrypted over HTTP.", key))
		}
	}
	var output, diagnostic bytes.Buffer
	exit, err := p.run(input, &output, &diagnostic, agentExecArgs(false, container, args...)...)
	for _, stream := range []struct {
		text   string
		writer io.Writer
	}{{output.String(), stdout}, {diagnostic.String(), stderr}} {
		text := redactProxyOutput(stream.text, key)
		if _, writeErr := io.WriteString(stream.writer, text); writeErr != nil {
			return 1, fmt.Errorf("cannot report proxy configuration result")
		}
	}
	if err != nil {
		return 1, fmt.Errorf("proxy configuration transport failed")
	}
	if exit != 0 {
		return 1, fmt.Errorf("proxy configuration failed")
	}
	const disclosure = "the key was not checked against the server."
	if key != "" && strings.Contains(disclosure, key) {
		// The two notices share only whitespace, which accepted keys cannot contain.
		for _, notice := range []string{disclosure, "SERVER KEY CHECK SKIPPED"} {
			if strings.Contains(notice, key) {
				continue
			}
			if _, writeErr := fmt.Fprintln(stdout, notice); writeErr != nil {
				return 1, fmt.Errorf("cannot report proxy configuration result")
			}
			break
		}
	}
	return 0, nil
}

// redactProxyOutput removes a validated key without reintroducing it in the marker or across replacement boundaries.
func redactProxyOutput(text, key string) string {
	if key == "" {
		return text
	}
	marker := "[redacted]"
	if strings.Contains(marker, key) {
		marker = "HIDDEN"
	}
	// Markers have disjoint character sets; spaces stop non-whitespace keys crossing their edges.
	return strings.ReplaceAll(text, key, " "+marker+" ")
}
