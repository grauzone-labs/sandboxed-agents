package manager

import (
	"flag"
	"fmt"
	"io"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

func loginGitHub(stdout, stderr io.Writer, effects Effects) int {
	if !verifyAgent(effects, stderr) {
		return 1
	}
	for _, args := range [][]string{
		{"auth", "login", "--hostname", "github.com", "--git-protocol", "https", "--web"},
		{"auth", "setup-git", "--hostname", "github.com"},
	} {
		if err := effects.Start(Process{Program: "gh", Args: args, Identity: agentIdentity, EnvironmentPolicy: GitHubEnvironment, Stdin: effects.Stdin, Stdout: stdout, Stderr: stderr}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	return 0
}

func configureGit(args []string, stdout, stderr io.Writer, effects Effects) int {
	workflowName := ""
	if len(args) > 0 {
		workflowName = args[0]
	}
	selection, err := catalog.Default.ResolveConfig(catalog.GitIntegration, workflowName)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	options := flag.NewFlagSet("git "+selection.Workflow, flag.ContinueOnError)
	options.SetOutput(stderr)
	values := make(map[string]*string)
	for _, option := range selection.Definition.Options {
		values[option] = options.String(option, "", "value for "+option)
	}
	if len(args) > 0 {
		if err := options.Parse(args[1:]); err != nil {
			return 2
		}
	}
	if options.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected arguments for git "+selection.Workflow)
		return 2
	}
	var configurations [][]string
	for _, setting := range selection.Definition.GitSettings {
		value := setting.Value
		if setting.Option != "" {
			value = *values[setting.Option]
			if value == "" {
				fmt.Fprintf(stderr, "git %s requires --%s\n", selection.Workflow, setting.Option)
				return 1
			}
		}
		configurations = append(configurations, []string{setting.Key, value})
	}

	if !verifyAgent(effects, stderr) {
		return 1
	}
	for _, configuration := range configurations {
		args := append([]string{"config", "--global", "--replace-all", "--"}, configuration...)
		err := effects.Start(Process{Program: "git", Args: args, Identity: agentIdentity, EnvironmentPolicy: GitConfigEnvironment, Stdout: stdout, Stderr: stderr})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	return 0
}
