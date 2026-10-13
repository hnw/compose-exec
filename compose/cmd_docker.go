package compose

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	"github.com/moby/moby/client"
)

func (c *Cmd) closeDockerIfOwned() {
	c.mu.Lock()
	if !c.dockerOwned || c.docker == nil {
		c.mu.Unlock()
		return
	}
	dc := c.docker
	c.docker = nil
	c.dockerOwned = false
	c.mu.Unlock()
	_ = dc.Close()
}

func (c *Cmd) ensureDockerClient() (dockerAPI, error) {
	c.mu.Lock()
	dc := c.docker
	c.mu.Unlock()
	if dc != nil {
		return dc, nil
	}
	cli, err := newDockerClient()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.docker != nil {
		existing := c.docker
		c.mu.Unlock()
		_ = cli.Close()
		return existing, nil
	}
	c.docker = cli
	c.dockerOwned = true
	c.mu.Unlock()
	return cli, nil
}

func imagePullRequired(
	ctx context.Context,
	dc dockerAPI,
	service types.ServiceConfig,
	now time.Time,
) (bool, error) {
	policy, duration, err := service.GetPullPolicy()
	if err != nil {
		return false, fmt.Errorf("compose: invalid pull_policy %q: %w", service.PullPolicy, err)
	}
	// GetPullPolicy treats unknown values as missing.
	switch service.PullPolicy {
	case "", types.PullPolicyAlways, types.PullPolicyNever, types.PullPolicyMissing,
		types.PullPolicyIfNotPresent, "daily", "weekly":
	case types.PullPolicyBuild:
		return false, fmt.Errorf(
			"compose: pull_policy %q is not supported (use a pre-built image)",
			service.PullPolicy,
		)
	default:
		if !strings.HasPrefix(service.PullPolicy, "every_") {
			return false, fmt.Errorf("compose: invalid pull_policy %q", service.PullPolicy)
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	inspected, err := dc.ImageInspect(ctx, service.Image)
	if err != nil && !cerrdefs.IsNotFound(err) {
		return false, err
	}
	switch policy {
	case types.PullPolicyAlways:
		return true, nil
	case types.PullPolicyNever:
		if err != nil {
			return false, fmt.Errorf(
				"compose: image %q is unavailable with pull_policy never: %w",
				service.Image,
				err,
			)
		}
		return false, nil
	case types.PullPolicyRefresh:
		return err != nil || now.After(inspected.Metadata.LastTagTime.Add(duration)), nil
	default:
		return err != nil, nil
	}
}

func pullImage(ctx context.Context, dc dockerAPI, service types.ServiceConfig) error {
	required, err := imagePullRequired(ctx, dc, service, time.Now())
	if err != nil {
		return err
	}
	if !required {
		return nil
	}

	pullOpts := client.ImagePullOptions{}
	if service.Platform != "" {
		p, parseErr := platforms.Parse(service.Platform)
		if parseErr != nil {
			return parseErr
		}
		pullOpts.Platforms = append(pullOpts.Platforms, p)
	}

	rc, err := dc.ImagePull(ctx, service.Image, pullOpts)
	if err != nil {
		return err
	}
	defer func() {
		_ = rc.Close()
	}()
	if waitErr := rc.Wait(ctx); waitErr != nil {
		return waitErr
	}
	return ctx.Err()
}

func stopAndKill(ctx context.Context, dc dockerAPI, id string, timeout time.Duration) error {
	seconds := int(timeout.Seconds())
	stopCtx, cancel := context.WithTimeout(ctx, timeout+1*time.Second)
	defer cancel()

	if _, err := dc.ContainerStop(
		stopCtx,
		id,
		client.ContainerStopOptions{Timeout: &seconds},
	); err != nil {
		killCtx, cancel2 := context.WithTimeout(ctx, 2*time.Second)
		defer cancel2()
		_, _ = dc.ContainerKill(killCtx, id, client.ContainerKillOptions{Signal: "SIGKILL"})
	}

	return nil
}

func forceRemoveContainer(ctx context.Context, dc dockerAPI, id string) error {
	rmCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := dc.ContainerRemove(rmCtx, id, client.ContainerRemoveOptions{Force: true})
	return err
}

func isAlreadyExistsErr(err error) bool {
	return cerrdefs.IsAlreadyExists(err) || strings.Contains(err.Error(), "already exists")
}
