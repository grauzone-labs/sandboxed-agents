// This executable is built by the test harness, never shipped with the CLI.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	os.Exit(code)
}
