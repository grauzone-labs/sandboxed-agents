// Fake gh serves byte-for-byte GitHub system-boundary fixtures, never a network.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) < 3 || os.Args[1] != "api" {
		fmt.Fprintln(os.Stderr, "unexpected gh invocation")
		os.Exit(1)
	}
	root := os.Getenv("GATE_FIXTURE")
	data, err := os.ReadFile(filepath.Join(root, "responses.json"))
	if err != nil {
		panic(err)
	}
	var responses map[string]string
	if err = json.Unmarshal(data, &responses); err != nil {
		panic(err)
	}
	file, ok := responses[os.Args[2]]
	if !ok {
		fmt.Fprintln(os.Stderr, "fixture API failed")
		os.Exit(1)
	}
	data, err = os.ReadFile(filepath.Join(root, file))
	if err != nil {
		panic(err)
	}
	os.Stdout.Write(data)
}
