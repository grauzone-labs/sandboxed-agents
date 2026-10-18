package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// markerPath optionally records execution and inherited environment for process-boundary tests.
var markerPath string

// main prints a fixed preview version and hash, optionally recording execution and its environment.
func main() {
	if markerPath != "" {
		data, err := json.Marshal(os.Environ())
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(markerPath, data, 0600); err != nil {
			panic(err)
		}
	}
	fmt.Println("sandboxed-agents 1.0.0-preview.20261012.1\nasset-sha256 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
}
