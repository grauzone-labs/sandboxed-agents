package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/grauzone-labs/sandboxed-agents/image"
)

func (p podmanTarget) exists(kind, name string) (bool, error) {
	_, err := p.query(kind, "exists", name)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func buildImage(p podmanTarget, output io.Writer) (string, error) {
	return image.Build(context.Background(), nil, func(ctx context.Context, args ...string) error {
		result, err := p.query(args...)
		if result != "" {
			fmt.Fprint(output, result)
		}
		return err
	})
}

func prepareUp(request Request, p podmanTarget, output io.Writer) error {
	// Sandbox lifecycle checks and mutations, including ADR-0007's lock,
	// belong to #14. This boundary only detects reuse and prepares new images.
	exists, err := p.exists("container", DerivedNames(request.Group, request.Name).Container)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("up: existing sandbox lifecycle is not implemented (#14)")
	}
	hash, err := image.Hash(image.Context())
	if err != nil {
		return err
	}
	tag := image.Tag(hash, request.Toolchains)
	exists, err = p.exists("image", tag)
	if err != nil {
		return err
	}
	if !exists {
		tag, err = buildImage(p, output)
		if err != nil {
			return err
		}
	}
	port, err := allocatePort(p, request.Options["port"])
	if err != nil {
		return err
	}
	return fmt.Errorf("up: image %s prepared; --publish 127.0.0.1:%d:22/tcp; sandbox creation is not implemented (#14)", tag, port)
}
