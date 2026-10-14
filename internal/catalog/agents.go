package catalog

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// InstallKind allows additional installation methods without changing npm entries.
type InstallKind string

const NPM InstallKind = "npm"

// Install declares an agent's package and installation method.
type Install struct {
	Kind    InstallKind
	Package string
}

// Probe declares a boolean field in an agent command's JSON authentication status.
type Probe struct {
	Args         []string
	BooleanField string
}

// Agent is shared installation and workflow data. Undelivered entries are unknown.
type Agent struct {
	Delivered      bool
	Install        Install
	Command        string
	LoginWorkflows map[string][]string
	LoginMessage   string
	StatusProbe    *Probe
	Documentation  string
}

// AgentNames returns delivered agents in stable display order.
func (c Catalog) AgentNames() []string {
	names := make([]string, 0, len(c.Agents))
	for name, agent := range c.Agents {
		if agent.Delivered {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// ResolveAgent rejects unknown and undelivered entries before system operations.
func (c Catalog) ResolveAgent(name string) (Agent, error) {
	agent, exists := c.Agents[name]
	if !exists || !agent.Delivered {
		return Agent{}, fmt.Errorf("unknown agent %q: %s", name, ValidNames(c.AgentNames()))
	}
	return agent, nil
}

var exactVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

// ValidateVersion accepts only exact package versions, never tags or npm ranges.
func ValidateVersion(version string) error {
	if !exactVersion.MatchString(version) {
		return fmt.Errorf("invalid version %q: --version requires an exact version such as 1.2.3", version)
	}
	withoutBuild, _, _ := strings.Cut(version, "+")
	_, prerelease, _ := strings.Cut(withoutBuild, "-")
	for _, identifier := range strings.Split(prerelease, ".") {
		if len(identifier) > 1 && identifier[0] == '0' && strings.Trim(identifier, "0123456789") == "" {
			return fmt.Errorf("invalid version %q: numeric prerelease identifiers cannot have leading zeros", version)
		}
	}
	return nil
}
