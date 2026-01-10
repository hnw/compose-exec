package compose

import (
	"context"
	"io"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type fakeDocker struct {
	stopCalls   int
	stopErr     bool
	killCalls   int
	removeCalls int
}

func (f *fakeDocker) ImageInspectWithRaw(
	_ context.Context,
	_ string,
) (image.InspectResponse, []byte, error) {
	return image.InspectResponse{}, nil, nil
}

func (f *fakeDocker) ImagePull(
	_ context.Context,
	_ string,
	_ image.PullOptions,
) (io.ReadCloser, error) {
	return io.NopCloser(&nopReader{}), nil
}

func (f *fakeDocker) ContainerCreate(
	_ context.Context,
	_ *container.Config,
	_ *container.HostConfig,
	_ *network.NetworkingConfig,
	_ *ocispec.Platform,
	_ string,
) (container.CreateResponse, error) {
	return container.CreateResponse{ID: "cid"}, nil
}

func (f *fakeDocker) ContainerStart(
	_ context.Context,
	_ string,
	_ container.StartOptions,
) error {
	return nil
}

func (f *fakeDocker) ContainerAttach(
	_ context.Context,
	_ string,
	_ container.AttachOptions,
) (dockertypes.HijackedResponse, error) {
	// Not used in unit tests.
	return dockertypes.HijackedResponse{}, nil
}

func (f *fakeDocker) ContainerWait(
	_ context.Context,
	_ string,
	_ container.WaitCondition,
) (<-chan container.WaitResponse, <-chan error) {
	respCh := make(chan container.WaitResponse, 1)
	errCh := make(chan error, 1)
	respCh <- container.WaitResponse{StatusCode: 0}
	return respCh, errCh
}

func (f *fakeDocker) ContainerInspect(
	_ context.Context,
	_ string,
) (container.InspectResponse, error) {
	return container.InspectResponse{}, nil
}

func (f *fakeDocker) ContainerStop(
	_ context.Context,
	_ string,
	_ container.StopOptions,
) error {
	f.stopCalls++
	if f.stopErr {
		return context.Canceled
	}
	return nil
}

func (f *fakeDocker) ContainerKill(_ context.Context, _ string, _ string) error {
	f.killCalls++
	return nil
}

func (f *fakeDocker) ContainerRemove(
	_ context.Context,
	_ string,
	_ container.RemoveOptions,
) error {
	f.removeCalls++
	return nil
}

type nopReader struct{}

func (n *nopReader) Read(_ []byte) (int, error) { return 0, io.EOF }

func TestMergeEnv_OrderAndOverride(t *testing.T) {
	got := mergeEnv(
		[]string{"A=1", "B=2"},
		[]string{"B=20", "C=3"},
	)
	want := []string{"A=1", "B=20", "C=3"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want=%d got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("idx=%d got=%q want=%q full=%v", i, got[i], want[i], got)
		}
	}
}

func TestServiceMounts_RelativeSourceResolved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path semantics differ")
	}

	svc := types.ServiceConfig{
		Volumes: []types.ServiceVolumeConfig{{
			Type:   types.VolumeTypeBind,
			Source: "./data",
			Target: "/work/data",
		}},
	}

	mounts, err := serviceMounts(svc, "/tmp/project")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(mounts) != 1 {
		t.Fatalf("mounts=%d", len(mounts))
	}

	want := filepath.Join("/tmp/project", "data")
	if mounts[0].Source != want {
		t.Fatalf("source=%q want=%q", mounts[0].Source, want)
	}
	if mounts[0].Target != "/work/data" {
		t.Fatalf("target=%q", mounts[0].Target)
	}
}

func TestStopAndKill_CallsDocker(t *testing.T) {
	fd := &fakeDocker{}
	_ = stopAndKill(context.Background(), fd, "cid", 2*time.Second)
	if fd.stopCalls != 1 {
		t.Fatalf("stopCalls=%d", fd.stopCalls)
	}
	if fd.killCalls != 0 {
		t.Fatalf("killCalls=%d", fd.killCalls)
	}
}

func TestStopAndKill_KillsOnStopError(t *testing.T) {
	fd := &fakeDocker{stopErr: true}
	_ = stopAndKill(context.Background(), fd, "cid", 2*time.Second)
	if fd.stopCalls != 1 {
		t.Fatalf("stopCalls=%d", fd.stopCalls)
	}
	if fd.killCalls != 1 {
		t.Fatalf("killCalls=%d", fd.killCalls)
	}
}
