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
			if _, err := fmt.Fprintln(stderr, proxyMessage("warning the proxy key travels unencrypted over http", key)); err != nil {
				return 1, fmt.Errorf("%s", proxyMessage("cannot report proxy configuration result", key))
			}
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
			return 1, fmt.Errorf("%s", proxyMessage("cannot report proxy configuration result", key))
		}
	}
	if err != nil {
		return 1, fmt.Errorf("%s", proxyMessage("proxy configuration transport failed", key))
	}
	if exit != 0 {
		return 1, fmt.Errorf("%s", proxyMessage("proxy configuration failed", key))
	}
	const disclosure = "the key was not checked against the server."
	if key != "" && strings.Contains(disclosure, key) {
		// The two notices share only whitespace, which accepted keys cannot contain.
		if _, writeErr := fmt.Fprintln(stdout, "SERVER KEY CHECK SKIPPED"); writeErr != nil {
			return 1, fmt.Errorf("%s", proxyMessage("cannot report proxy configuration result", key))
		}
	}
	return 0, nil
}

// proxyMessage keeps a message of lowercase ASCII words and spaces readable without containing the validated key.
func proxyMessage(message, key string) string {
	if key != "" && strings.Contains(message, key) {
		// Uppercase and lowercase ASCII words share only spaces; accepted keys contain no whitespace.
		return strings.ToUpper(message)
	}
	return message
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
