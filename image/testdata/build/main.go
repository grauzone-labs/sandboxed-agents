// This executable exercises the public image build API; it is never shipped.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/grauzone-labs/sandboxed-agents/image"
)

// main builds the base image through the public API and prints its asset hash and image tags.
func main() {
	tag, err := image.Build(context.Background(), nil, func(ctx context.Context, args ...string) error {
		command := exec.CommandContext(ctx, "podman", args...)
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		return command.Run()
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	hash, err := image.Hash(image.Context())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("asset-sha256", hash)
	fmt.Println("image-tag", tag)
	for _, toolchains := range [][]string{{"dotnet", "playwright"}, {"azure", "native"}} {
		fmt.Println("image-tag", image.Tag(hash, toolchains))
	}
}
