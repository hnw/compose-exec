package compose

import (
	"context"
	"fmt"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
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
	containers, err := cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", "com.docker.compose.project="+projectName),
	})
	if err != nil {
		return fmt.Errorf("compose: failed to list containers: %w", err)
	}

	for _, c := range containers.Items {
		_, rmErr := cli.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true})
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
	list, err := cli.NetworkList(ctx, client.NetworkListOptions{
		Filters: client.Filters{}.Add("label", "com.docker.compose.project="+projectName),
	})
	if err != nil {
		errs = append(errs, fmt.Sprintf("failed to list networks: %v", err))
	} else {
		for _, n := range list.Items {
			_, err := cli.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{})
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
