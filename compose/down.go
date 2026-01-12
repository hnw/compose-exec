package compose

import (
	"context"
	"fmt"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
)

// Down cleans up all resources (containers and networks) associated with the project.
// It ignores "not found" errors for idempotency.
func Down(ctx context.Context, projectName string) error {
	if projectName == "" {
		return fmt.Errorf("compose: project name is required")
	}

	cli, err := newDockerClient()
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()

	var errs []string

	// ---------------------------------------------------------
	// 1. Remove Containers (MUST be done before removing networks)
	// ---------------------------------------------------------
	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", "com.docker.compose.project="+projectName),
		),
	})
	if err != nil {
		return fmt.Errorf("compose: failed to list containers: %w", err)
	}

	for _, c := range containers {
		rmErr := cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
		if rmErr == nil {
			continue
		}
		if cerrdefs.IsNotFound(rmErr) ||
			strings.Contains(strings.ToLower(rmErr.Error()), "not found") {
			continue
		}
		errs = append(errs, fmt.Sprintf("container %s: %v", c.Names, rmErr))
	}

	// ---------------------------------------------------------
	// 2. Remove Networks
	// ---------------------------------------------------------
	list, err := cli.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", "com.docker.compose.project="+projectName)),
	})
	if err != nil {
		errs = append(errs, fmt.Sprintf("failed to list networks: %v", err))
	} else {
		for _, n := range list {
			err := cli.NetworkRemove(ctx, n.ID)
			if err == nil {
				continue
			}
			if cerrdefs.IsNotFound(err) ||
				strings.Contains(strings.ToLower(err.Error()), "not found") {
				continue
			}
			errs = append(errs, fmt.Sprintf("network %s: %v", n.Name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("compose: down errors: %s", strings.Join(errs, "; "))
	}
	return nil
}
