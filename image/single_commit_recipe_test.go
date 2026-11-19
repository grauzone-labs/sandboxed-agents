package image_test

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/image"
	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// singleCommitRecipeError is a test-only structural guard for single-stage recipes and same-instruction SSH host-key removal.
func singleCommitRecipeError(recipe string) error {
	stages := 0
	instruction := ""
	install := regexp.MustCompile(`(?i)\bapt(?:-get)?\s+install\b`)
	remove := regexp.MustCompile(`(?:^|&&|;)\s*rm\s+-[a-zA-Z]*f[a-zA-Z]*\s+/etc/ssh/ssh_host_\*(?:\s|$|&&|;)`)
	for _, line := range strings.Split(recipe, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		continuation := strings.HasSuffix(line, "\\")
		instruction += " " + strings.TrimSuffix(line, "\\")
		if continuation {
			continue
		}
		fields := strings.Fields(instruction)
		if len(fields) == 0 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "FROM":
			stages++
		case "RUN":
			if strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(instruction), fields[0])), "[") {
				return fmt.Errorf("JSON RUN requires explicit guard support")
			}
			// Ignore quoted text so echo cannot imitate an executed cleanup command.
			plain := []rune(instruction)
			var quote rune
			for i, c := range plain {
				if quote == 0 && c == '#' && (i == 0 || plain[i-1] == ' ') {
					for j := i; j < len(plain); j++ {
						plain[j] = ' '
					}
					break
				}
				if quote != 0 {
					plain[i] = ' '
					if c == quote {
						quote = 0
					}
					continue
				}
				if c == '\'' || c == '"' {
					quote = c
					plain[i] = ' '
				}
			}
			command := string(plain)
			start := install.FindStringIndex(command)
			if strings.Contains(instruction, "openssh-server") {
				if start == nil {
					return fmt.Errorf("unrecognized SSH server installation")
				}
				end := remove.FindStringIndex(command)
				if end == nil || end[0] < start[1] {
					return fmt.Errorf("SSH keys must be removed in the installing RUN")
				}
			}
		case "ARG", "ENV", "COPY", "ADD", "LABEL", "USER", "WORKDIR", "ENTRYPOINT", "CMD", "EXPOSE", "VOLUME", "STOPSIGNAL", "SHELL", "HEALTHCHECK":
		default:
			return fmt.Errorf("unsupported recipe instruction %q", fields[0])
		}
		instruction = ""
	}
	if instruction != "" || stages != 1 {
		return fmt.Errorf("require exactly one complete stage, got %d", stages)
	}
	return nil
}

// TestSingleCommitRecipeGuardRejectsUnsafeFixtures proves the recipe-policy seam catches multiple stages and SSH keys deleted in a later instruction rather than the installing instruction.
func TestSingleCommitRecipeGuardRejectsUnsafeFixtures(t *testing.T) {
	for _, recipe := range []string{
		"FROM base\nFROM other\n",
		"FROM base\nRUN apt-get install -y openssh-server\nRUN rm -f /etc/ssh/ssh_host_*\n",
		"FROM base\nRUN rm -f /etc/ssh/ssh_host_* && apt-get install -y openssh-server\n",
		"FROM base\nRUN apt-get install -y openssh-server && echo 'rm -f /etc/ssh/ssh_host_*'\n",
		"FROM base\nRUN apt-get install -y openssh-server # && rm -f /etc/ssh/ssh_host_*\n",
		"FROM base\nRUN [\"apt-get\",\"install\",\"openssh-server\"]\n",
	} {
		if err := singleCommitRecipeError(recipe); err == nil {
			t.Fatalf("unsafe recipe accepted: %q", recipe)
		}
	}
}

// TestSingleCommitCatalogRecipes proves every generated catalog combination and embedded base recipe stays single-stage and removes SSH keys in its installing instruction at the public image-build seam.
func TestSingleCommitCatalogRecipes(t *testing.T) {
	for mask := 0; mask < 1<<len(catalog.Default.Toolchains); mask++ {
		set := []string{}
		for i, name := range catalog.Default.Toolchains {
			if mask&(1<<i) != 0 {
				set = append(set, name)
			}
		}
		t.Run(strings.Join(set, "+"), func(t *testing.T) {
			_, err := image.BuildWithToolchains(context.Background(), nil, set, func(_ context.Context, args ...string) error {
				data, err := os.ReadFile(args[2])
				if err != nil {
					return err
				}
				return singleCommitRecipeError(string(data))
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
