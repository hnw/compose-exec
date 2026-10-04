package compose

import (
	"context"
	"io"
	"strings"
	"time"

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

func pullImage(ctx context.Context, dc dockerAPI, ref, platform string) error {
	if _, err := dc.ImageInspect(ctx, ref); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return err
	}

	pullOpts := client.ImagePullOptions{}
	if platform != "" {
		p, err := platforms.Parse(platform)
		if err != nil {
			return err
		}
		pullOpts.Platforms = append(pullOpts.Platforms, p)
	}

	rc, err := dc.ImagePull(ctx, ref, pullOpts)
	if err != nil {
		return err
	}
	defer func() {
		_ = rc.Close()
	}()
	_, _ = io.Copy(io.Discard, rc)
	return nil
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
