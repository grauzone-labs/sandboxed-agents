package cli

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestEveryCommandHasHelp proves every entry of the commands table in parse.go is listed by its root
// or group help and has help with a description, a usage line that names its command path, an
// example that Parse accepts, and an
// entry for exactly the options the table defines, each with a value label when it takes a value,
// so a new command cannot ship without help. It exercises the commands table and the help table.
func TestEveryCommandHasHelp(t *testing.T) {
	topics := helpTopics()
	for command, definition := range commands {
		entry, found := topics[command]
		if !found {
			t.Errorf("command %q has no help", command)
			continue
		}
		group, subcommand, grouped := strings.Cut(command, " ")
		if (grouped && !slices.Contains(groupSubcommands[group], subcommand)) || (!grouped && !slices.Contains(topLevelCommands, command)) {
			t.Errorf("command %q is not listed in the help of its root or group", command)
		}
		if entry.summary == "" || len(entry.usage) == 0 || len(entry.examples) == 0 {
			t.Errorf("help of %q needs a description, a usage line, and an example: %+v", command, entry)
		}
		for _, usage := range entry.usage {
			if !strings.HasPrefix(usage, command+" ") && usage != command {
				t.Errorf("help usage %q of %q does not start with its command path", usage, command)
			}
		}
		for _, example := range entry.examples {
			words := strings.Fields(example)
			if !strings.HasPrefix(example, command+" ") && example != command {
				t.Errorf("help example %q of %q does not start with its command path", example, command)
			} else if _, err := Parse(words, "default"); err != nil {
				t.Errorf("help example %q of %q is rejected by the parser: %v", example, command, err)
			}
		}
		want := map[string]bool{}
		for name, takesValue := range definition.options {
			want[name] = takesValue
		}
		if command == "update" {
			want["all"] = false
		}
		got := map[string]bool{}
		for _, option := range entry.options {
			got[option.name] = option.value != ""
		}
		if !maps.Equal(got, want) {
			t.Errorf("help options of %q are %v; the commands table defines %v (true means a value label)", command, got, want)
		}
	}
	for topic := range topics {
		if _, known := commands[topic]; !known && topic != "" && !slices.Contains(commandGroups, topic) {
			t.Errorf("help topic %q names no command", topic)
		}
	}
}

// TestHelpUsageFollowsCommandReferenceGrammar proves every fixed usage line of the help table
// appears in the grammar block of docs/command-reference.md, so help and the reference cannot drift.
// It exercises the help table against the command reference.
func TestHelpUsageFollowsCommandReferenceGrammar(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "command-reference.md"))
	if err != nil {
		t.Fatal(err)
	}
	grammar := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		grammar[line] = true
	}
	for command := range commands {
		if command == "integrations login" || command == "integrations config" {
			// Their reference lines are concrete catalog forms; help derives them from the catalog.
			continue
		}
		for _, usage := range helpTopics()[command].usage {
			if !grammar["sandboxed-agents "+usage] {
				t.Errorf("help usage %q is not a grammar line of the command reference", usage)
			}
		}
	}
}

// TestHelpResourceDefaultsMatchParser proves each default that up's help names for a resource limit
// is the limit Parse applies when the option is omitted. It exercises the help table against the
// Parse seam.
func TestHelpResourceDefaultsMatchParser(t *testing.T) {
	checked := 0
	for _, option := range helpTopics()["up"].options {
		switch option.name {
		case "memory", "cpus", "pids-limit", "shm-size":
			request, err := Parse([]string{"up", "agent01", "--" + option.name, option.defaultValue}, "default")
			if err != nil || request.Resources != DefaultResources() {
				t.Errorf("--%s %s gives %+v (%v); the omitted option gives %+v", option.name, option.defaultValue, request.Resources, err, DefaultResources())
			}
			checked++
		}
	}
	if checked != 4 {
		t.Fatalf("up help names %d resource limits; want 4", checked)
	}
}
