package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// TestCommandReferenceMatchesCommandTree proves documented commands, positional ranges, and value/switch options match the executable grammar at the parser definition and Parse seams.
func TestCommandReferenceMatchesCommandTree(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "command-reference.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := commandReferenceError(string(data)); err != nil {
		t.Fatal(err)
	}
	for name, document := range map[string]string{
		"missing-command":        strings.ReplaceAll(string(data), "sandboxed-agents version", "OMITTED version"),
		"extra-command":          string(data) + "\nsandboxed-agents nonexistent\n",
		"extra-argument":         strings.ReplaceAll(string(data), "sandboxed-agents version", "sandboxed-agents version NAME"),
		"extra-option":           string(data) + "\nsandboxed-agents start NAME [--unknown-option]\n",
		"option-value":           strings.ReplaceAll(string(data), "--force", "--force VALUE"),
		"optional-check-name":    strings.ReplaceAll(string(data), "check [NAME]", "check"),
		"required-check-name":    strings.ReplaceAll(string(data), "check [NAME]", "check NAME"),
		"optional-workspace":     strings.ReplaceAll(string(data), "NAME [WORKSPACE]", "NAME"),
		"optional-workflow":      strings.ReplaceAll(string(data), "AGENT [WORKFLOW]", "AGENT"),
		"variadic-run-arguments": strings.ReplaceAll(string(data), "AGENT [ARG...]", "AGENT [ARG]"),
	} {
		if err := commandReferenceError(document); err == nil {
			t.Errorf("reference check accepted %s drift", name)
		}
	}
}

func commandReferenceError(document string) error {
	type argumentRange struct{ min, max int }
	ranges := map[string]argumentRange{}
	documented := map[string]map[string]bool{}
	optionPattern := regexp.MustCompile(`--([a-z][a-z-]*)(?:[ =]([A-Z][A-Z_]*))?`)
	optionalPattern := regexp.MustCompile(`\[[^\]]*\]`)
	for _, line := range strings.Split(document, "\n") {
		if !strings.HasPrefix(line, "sandboxed-agents ") {
			continue
		}
		words := strings.Fields(strings.TrimPrefix(line, "sandboxed-agents "))
		if len(words) == 0 {
			return fmt.Errorf("incomplete command grammar: %s", line)
		}
		command := words[0]
		if command == "agents" || command == "integrations" {
			if len(words) < 2 {
				return fmt.Errorf("incomplete command grammar: %s", line)
			}
			command += " " + words[1]
		}
		if documented[command] == nil {
			documented[command] = map[string]bool{}
		}
		for _, match := range optionPattern.FindAllStringSubmatch(line, -1) {
			if previous, exists := documented[command][match[1]]; exists && previous != (match[2] != "") {
				return fmt.Errorf("command reference gives conflicting value/switch forms for %s --%s", command, match[1])
			}
			documented[command][match[1]] = match[2] != ""
		}
		// The mandatory form of every grammar line must also be a real
		// invocation. This catches positional and fixed workflow drift that
		// a command/option inventory alone cannot find.
		example := strings.Fields(optionalPattern.ReplaceAllString(strings.TrimPrefix(line, "sandboxed-agents "), ""))
		for index, word := range example {
			switch word {
			case "NAME":
				example[index] = "agent01"
			case "AGENT":
				example[index] = "claude"
			case "URL":
				example[index] = "https://dev.azure.com/contoso"
			}
		}
		parsed, err := Parse(example, "default")
		if err != nil {
			return fmt.Errorf("command reference grammar %q is rejected by the parser: %w", line, err)
		}
		// update --all has its own zero-positional branch, separate from the
		// named update definition in the tree. Parse above checks that form.
		if command == "update" && parsed.Options["all"] != "" {
			continue
		}
		positionals := strings.Fields(optionPattern.ReplaceAllString(strings.TrimPrefix(line, "sandboxed-agents "), ""))[len(strings.Fields(command)):]
		form := argumentRange{}
		for _, word := range positionals {
			if strings.Trim(word, "[]|") == "" {
				continue
			}
			if !strings.HasPrefix(word, "[") {
				form.min++
			}
			if strings.Contains(word, "...") || form.max == -1 {
				form.max = -1
			} else {
				form.max++
			}
		}
		if previous, exists := ranges[command]; exists {
			form.min = min(form.min, previous.min)
			if previous.max == -1 || form.max == -1 {
				form.max = -1
			} else {
				form.max = max(form.max, previous.max)
			}
		}
		ranges[command] = form
	}
	want := map[string]map[string]bool{}
	for command, definition := range commands {
		options := map[string]bool{}
		for name, takesValue := range definition.options {
			options[name] = takesValue
		}
		want[command] = options
	}
	want["update"]["all"] = false
	for _, name := range catalog.Default.ConfigOptionNames() {
		want["integrations config"][name] = true
	}
	if !reflect.DeepEqual(documented, want) {
		return fmt.Errorf("command reference grammar differs from command tree\ndocumented: %#v\nexecutable: %#v", documented, want)
	}
	for command, definition := range commands {
		minimum := definition.min
		if command == "integrations config" {
			// The catalog requires an explicit workflow after NAME and git;
			// the tree's two-argument form exists to diagnose its omission.
			minimum = 3
		}
		if got := ranges[command]; got.min != minimum || got.max != definition.max {
			return fmt.Errorf("command reference %s positional range is %d..%d; executable requires %d..%d", command, got.min, got.max, minimum, definition.max)
		}
	}
	return nil
}
