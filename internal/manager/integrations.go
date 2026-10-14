package manager

import (
	"flag"
	"fmt"
	"io"
)

func configureGit(args []string, stdout, stderr io.Writer, effects Effects) int {
	var configurations [][]string
	switch args[0] {
	case "credentials":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "git credentials takes no options")
			return 2
		}
		configurations = [][]string{{"credential.helper", "store --file=/home/agent/.git-credentials"}}
	case "identity":
		options := flag.NewFlagSet("git identity", flag.ContinueOnError)
		options.SetOutput(stderr)
		name := options.String("name", "", "commit name")
		email := options.String("email", "", "commit email")
		if err := options.Parse(args[1:]); err != nil {
			return 2
		}
		if options.NArg() != 0 {
			fmt.Fprintln(stderr, "git identity takes only --name and --email")
			return 2
		}
		if *name == "" || *email == "" {
			fmt.Fprintln(stderr, "git identity requires both --name and --email")
			return 1
		}
		configurations = [][]string{{"user.name", *name}, {"user.email", *email}}
	default:
		fmt.Fprintln(stderr, "git config workflows: identity, credentials")
		return 2
	}
	if !verifyAgent(effects, stderr) {
		return 1
	}
	for _, configuration := range configurations {
		args := append([]string{"config", "--global", "--replace-all", "--"}, configuration...)
		err := effects.Start(Process{Program: "git", Args: args, Identity: agentIdentity, Stdout: stdout, Stderr: stderr})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	return 0
}
