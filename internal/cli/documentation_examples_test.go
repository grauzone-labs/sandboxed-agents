package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
)

// TestQuickGuideCommandsParse proves every executable command line in both quick guides is accepted at the public cli.Parse seam, including quoted values.
func TestQuickGuideCommandsParse(t *testing.T) {
	for _, name := range []string{"quick-guide-linux.md", "quick-guide-windows.md"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "docs", name))
			if err != nil {
				t.Fatal(err)
			}
			if err := quickGuideCommandsError(string(data)); err != nil {
				t.Fatal(err)
			}
			if err := quickGuideCommandsError(string(data) + "\nsandboxed-agents start agent01 --unknown-option\n"); err == nil {
				t.Fatal("guide check accepted a command the parser rejects")
			}
		})
	}
}

// quickGuideCommandsError checks that every executable quick-guide example is accepted by the
// public CLI parser.
func quickGuideCommandsError(document string) error {
	count := 0
	for index, line := range strings.Split(document, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "sandboxed-agents ") {
			continue
		}
		count++
		args, err := guideWords(strings.TrimPrefix(line, "sandboxed-agents "))
		if err == nil {
			_, err = cli.Parse(args, "default")
		}
		if err != nil {
			return fmt.Errorf("line %d: %s: %w", index+1, line, err)
		}
	}
	if count == 0 {
		return fmt.Errorf("guide contains no executable command lines")
	}
	return nil
}

// guideWords reads the guides' single-line commands with literal single/double
// quoted arguments. Shell expansion and pipelines are intentionally unsupported.
func guideWords(line string) ([]string, error) {
	var words []string
	var word strings.Builder
	var quote rune
	started := false
	for _, char := range line {
		switch {
		case quote != 0:
			if char == quote {
				quote = 0
			} else {
				word.WriteRune(char)
			}
		case char == '\'' || char == '"':
			quote, started = char, true
		case unicode.IsSpace(char):
			if started {
				words = append(words, word.String())
				word.Reset()
				started = false
			}
		case strings.ContainsRune("|;&`$\\", char):
			return nil, fmt.Errorf("unsupported shell syntax %q: keep executable examples literal and on one line", char)
		default:
			word.WriteRune(char)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed quote")
	}
	if started {
		words = append(words, word.String())
	}
	return words, nil
}
