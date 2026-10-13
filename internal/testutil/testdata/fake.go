// This executable is built by the test harness, never shipped with the CLI.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

func main() {
	prefix := "TEST_" + strings.ToUpper(strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe"))
	file, err := os.OpenFile(os.Getenv(prefix+"_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(125)
	}
	err = json.NewEncoder(file).Encode(os.Args[1:])
	file.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(125)
	}
	fmt.Fprint(os.Stdout, os.Getenv(prefix+"_OUTPUT"))
	code, err := strconv.Atoi(os.Getenv(prefix + "_EXIT"))
	if err != nil {
		code = 0
	}
	if script := os.Getenv(prefix + "_RESPONSES"); script != "" {
		var responses map[string]struct {
			Output string
			Exit   int
		}
		if err := json.Unmarshal([]byte(script), &responses); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		key := strings.Join(os.Args[1:], " ")
		response, ok := responses[key]
		if !ok {
			var keys []string
			for candidate := range responses {
				keys = append(keys, candidate)
			}
			slices.Sort(keys)
			for _, candidate := range keys {
				if strings.HasSuffix(candidate, " *") && strings.HasPrefix(key, strings.TrimSuffix(candidate, "*")) {
					response, ok = responses[candidate], true
					break
				}
			}
		}
		if ok {
			fmt.Fprint(os.Stdout, response.Output)
			code = response.Exit
		} else {
			fmt.Fprintln(os.Stderr, "unscripted fake call", os.Args[1:])
			code = 125
		}
	}
	if path := os.Getenv(prefix + "_ENV_LOG"); path != "" {
		data, _ := json.Marshal(os.Environ())
		_ = os.WriteFile(path, data, 0600)
	}
	os.Exit(code)
}
