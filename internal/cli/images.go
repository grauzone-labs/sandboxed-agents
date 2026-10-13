package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/grauzone-labs/sandboxed-agents/image"
)

type resourceKind string

const (
	containerResource resourceKind = "container"
	imageResource     resourceKind = "image"
)

func (p podmanTarget) exists(kind resourceKind, name string) (bool, error) {
	_, err := p.query(string(kind), "exists", name)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func buildImage(p podmanTarget, toolchains []string, output io.Writer) (string, error) {
	return image.BuildWithToolchains(context.Background(), nil, toolchains, func(ctx context.Context, args ...string) error {
		result, err := p.query(args...)
		if result != "" {
			fmt.Fprint(output, result)
		}
		return err
	})
}

func prepareUp(request Request, p podmanTarget, output io.Writer) error {
	// Look up the container, validate/allocate its port from inventory, build a
	// missing image, then report preparation. Container and volume creation
	// belong to #14; the caller holds ADR-0007's lifecycle lock.
	exists, err := p.exists(containerResource, DerivedNames(request.Group, request.Name).Container)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("up: existing sandbox lifecycle is not implemented (#14)")
	}
	port, err := allocatePort(p, request.Options["port"])
	if err != nil {
		return err
	}
	hash, err := image.Hash(image.Context())
	if err != nil {
		return err
	}
	tag := image.Tag(hash, request.Toolchains)
	exists, err = p.exists(imageResource, tag)
	if err != nil {
		return err
	}
	if !exists {
		tag, err = buildImage(p, request.Toolchains, output)
		if err != nil {
			return err
		}
	}
	return fmt.Errorf("up: image %s prepared; --publish 127.0.0.1:%d:22/tcp; sandbox creation is not implemented (#14)", tag, port)
}
