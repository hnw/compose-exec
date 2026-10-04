package compose

import (
	"context"

	"github.com/moby/moby/client"
)

// dockerAPI is the subset of the Docker Engine API client used by compose-exec.
// The signatures mirror github.com/moby/moby/client (Docker 29 generation).
type dockerAPI interface {
	ImageInspect(
		ctx context.Context,
		imageID string,
		options ...client.ImageInspectOption,
	) (client.ImageInspectResult, error)
	ImagePull(
		ctx context.Context,
		ref string,
		options client.ImagePullOptions,
	) (client.ImagePullResponse, error)

	ContainerCreate(
		ctx context.Context,
		options client.ContainerCreateOptions,
	) (client.ContainerCreateResult, error)
	ContainerStart(
		ctx context.Context,
		containerID string,
		options client.ContainerStartOptions,
	) (client.ContainerStartResult, error)
	ContainerAttach(
		ctx context.Context,
		containerID string,
		options client.ContainerAttachOptions,
	) (client.ContainerAttachResult, error)
	ContainerWait(
		ctx context.Context,
		containerID string,
		options client.ContainerWaitOptions,
	) client.ContainerWaitResult
	ContainerInspect(
		ctx context.Context,
		containerID string,
		options client.ContainerInspectOptions,
	) (client.ContainerInspectResult, error)
	ContainerStop(
		ctx context.Context,
		containerID string,
		options client.ContainerStopOptions,
	) (client.ContainerStopResult, error)
	ContainerKill(
		ctx context.Context,
		containerID string,
		options client.ContainerKillOptions,
	) (client.ContainerKillResult, error)
	ContainerRemove(
		ctx context.Context,
		containerID string,
		options client.ContainerRemoveOptions,
	) (client.ContainerRemoveResult, error)
	ContainerList(
		ctx context.Context,
		options client.ContainerListOptions,
	) (client.ContainerListResult, error)

	NetworkList(
		ctx context.Context,
		options client.NetworkListOptions,
	) (client.NetworkListResult, error)
	NetworkCreate(
		ctx context.Context,
		name string,
		options client.NetworkCreateOptions,
	) (client.NetworkCreateResult, error)
	NetworkRemove(
		ctx context.Context,
		networkID string,
		options client.NetworkRemoveOptions,
	) (client.NetworkRemoveResult, error)
	VolumeCreate(
		ctx context.Context,
		options client.VolumeCreateOptions,
	) (client.VolumeCreateResult, error)
	Close() error
}

func newDockerClient() (dockerAPI, error) {
	opts, err := dockerClientOpts()
	if err != nil {
		return nil, err
	}
	return client.New(opts...)
}
