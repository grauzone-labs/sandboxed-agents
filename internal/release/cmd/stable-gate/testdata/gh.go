// Fake gh serves byte-for-byte GitHub system-boundary fixtures, never a network.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// main simulates CLI version reporting and preview attestation policy, and logs
// API requests while serving configured responses without network access.
func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		version, err := os.ReadFile(filepath.Join(os.Getenv("GATE_FIXTURE"), "gh-version"))
		if os.IsNotExist(err) {
			version = []byte("2.102.0")
		} else if err != nil {
			panic(err)
		}
		fmt.Printf("gh version %s\n", version)
		return
	}
	if len(os.Args) > 2 && os.Args[1] == "attestation" && os.Args[2] == "verify" {
		verifyPreview(os.Args[3:])
		return
	}
	if len(os.Args) < 3 || os.Args[1] != "api" {
		fmt.Fprintln(os.Stderr, "unexpected gh invocation")
		os.Exit(1)
	}
	root := os.Getenv("GATE_FIXTURE")
	log, err := os.OpenFile(filepath.Join(root, "requests.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	if _, err := fmt.Fprintln(log, os.Args[2]); err != nil {
		panic(err)
	}
	if err := log.Close(); err != nil {
		panic(err)
	}
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

// verifyPreview models attestation verification, requiring the exact bytes and every expected source binding before executable permission or execution.
func verifyPreview(args []string) {
	root := os.Getenv("GATE_FIXTURE")
	if len(args) == 0 {
		panic("missing preview path")
	}
	flags := flag.NewFlagSet("verify", flag.ExitOnError)
	repo := flags.String("repo", "", "")
	workflow := flags.String("signer-workflow", "", "")
	commit := flags.String("source-digest", "", "")
	ref := flags.String("source-ref", "", "")
	deny := flags.Bool("deny-self-hosted-runners", false, "")
	if err := flags.Parse(args[1:]); err != nil {
		panic(err)
	}
	log, err := os.OpenFile(filepath.Join(root, "requests.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	if _, err := fmt.Fprintln(log, "verify "+*ref); err != nil {
		panic(err)
	}
	if err := log.Close(); err != nil {
		panic(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "attestations.json"))
	if err != nil {
		panic(err)
	}
	var proofs map[string]map[string]string
	if err := json.Unmarshal(data, &proofs); err != nil {
		panic(err)
	}
	proof := proofs[*ref]
	sourceRef := proof["ref"]
	if sourceRef == "" {
		sourceRef = *ref
	}
	info, err := os.Stat(args[0])
	if err != nil {
		panic(err)
	}
	binary, err := os.ReadFile(args[0])
	if err != nil {
		panic(err)
	}
	if flags.NArg() != 0 || proof == nil || *repo != proof["repository"] || *workflow != proof["workflow"] || *ref != sourceRef || *commit != proof["commit"] || !*deny || proof["hosted"] != "true" || fmt.Sprintf("%x", sha256.Sum256(binary)) != proof["sha256"] || info.Mode().Perm()&0111 != 0 {
		fmt.Fprintln(os.Stderr, "unverified preview provenance")
		os.Exit(1)
	}
	if _, err := os.Stat(filepath.Join(root, "executed-preview.json")); !os.IsNotExist(err) {
		panic("preview executed before verification")
	}
	if err := os.WriteFile(filepath.Join(root, "verified-preview"), []byte(*ref), 0600); err != nil {
		panic(err)
	}
}
