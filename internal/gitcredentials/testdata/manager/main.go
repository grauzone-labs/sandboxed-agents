package main

import (
	"github.com/grauzone-labs/sandboxed-agents/internal/gitcredentials"
	"os"
	"path/filepath"
)

// main exposes the real store adapters for isolated-home tests without requiring sandbox UID 1000.
func main() {
	s := gitcredentials.Store{Home: os.Getenv("HOME"), Manager: os.Args[0]}
	var err error
	if filepath.Base(os.Args[0]) == "sandboxed-agents-pinentry" {
		socket, e := s.AgentSocket()
		err = e
		if err == nil {
			err = gitcredentials.Pinentry(os.Stdin, os.Stdout, socket)
		}
	} else {
		err = s.Helper(os.Args[2:], os.Stdin, os.Stdout)
	}
	if err != nil {
		os.Exit(1)
	}
}
