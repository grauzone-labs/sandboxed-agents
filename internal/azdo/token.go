// Package azdo shares Azure DevOps token input rules between the host and manager.
package azdo

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// ReadToken reads the first input line without including input failures in diagnostics.
func ReadToken(input io.Reader) (string, error) {
	if input == nil {
		return NormalizeToken("")
	}
	value, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("cannot read Azure DevOps token")
	}
	return NormalizeToken(value)
}

// NormalizeToken removes surrounding whitespace and rejects empty or multiline credential-protocol values.
func NormalizeToken(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("Azure DevOps token is required and must be a single line")
	}
	return value, nil
}
