package compose

import (
	"context"
	"io"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type fakeDocker struct {
	stopCalls   int
	stopErr     bool
	killCalls   int
	removeCalls int

	volumeCreateCalls []volume.CreateOptions
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

func (f *fakeDocker) ContainerList(
	_ context.Context,
	_ container.ListOptions,
) ([]container.Summary, error) {
	return []container.Summary{}, nil
}

func (f *fakeDocker) NetworkList(
	_ context.Context,
	_ network.ListOptions,
) ([]network.Summary, error) {
	return []network.Summary{}, nil
}

func (f *fakeDocker) NetworkCreate(
	_ context.Context,
	_ string,
	_ network.CreateOptions,
) (network.CreateResponse, error) {
	return network.CreateResponse{ID: "fake-network-id"}, nil
}

func (f *fakeDocker) NetworkRemove(_ context.Context, _ string) error {
	return nil
}

func (f *fakeDocker) VolumeCreate(
	_ context.Context,
	options volume.CreateOptions,
) (volume.Volume, error) {
	f.volumeCreateCalls = append(f.volumeCreateCalls, options)
	return volume.Volume{Name: options.Name}, nil
}

func (f *fakeDocker) Close() error {
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

	mounts, err := serviceMounts(svc, "/tmp/project", "proj")
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

func TestServiceMounts_NamedVolumeResolved(t *testing.T) {
	svc := types.ServiceConfig{
		Volumes: []types.ServiceVolumeConfig{{
			Type:   types.VolumeTypeVolume,
			Source: "db_data",
			Target: "/data",
		}},
	}

	mounts, err := serviceMounts(svc, "/tmp/project", "myproj")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(mounts) != 1 {
		t.Fatalf("mounts=%d", len(mounts))
	}
	if mounts[0].Type != "volume" {
		t.Fatalf("type=%q want=%q", mounts[0].Type, "volume")
	}
	if mounts[0].Source != "myproj_db_data" {
		t.Fatalf("source=%q want=%q", mounts[0].Source, "myproj_db_data")
	}
	if mounts[0].Target != "/data" {
		t.Fatalf("target=%q", mounts[0].Target)
	}
}

func TestCmd_ensureVolumes_CreatesTopLevelProjectVolumes(t *testing.T) {
	fd := &fakeDocker{}

	proj := &types.Project{
		Name: "myproj",
		Volumes: types.Volumes{
			"db_data": types.VolumeConfig{},
		},
	}

	s := NewService(proj, types.ServiceConfig{Name: "alpine", Image: "alpine:latest"})

	c := &Cmd{Service: s.config, service: s}

	if err := c.ensureVolumes(context.Background(), fd); err != nil {
		t.Fatalf("ensureVolumes: %v", err)
	}
	if len(fd.volumeCreateCalls) != 1 {
		t.Fatalf("calls=%d", len(fd.volumeCreateCalls))
	}
	if fd.volumeCreateCalls[0].Name != "myproj_db_data" {
		t.Fatalf("name=%q want=%q", fd.volumeCreateCalls[0].Name, "myproj_db_data")
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

func TestCmd_resolveCommand_FallbackOnlyWhenArgsEmpty(t *testing.T) {
	svc := types.ServiceConfig{Command: types.ShellCommand{"echo", "from-yaml"}}

	t.Run("nil args falls back", func(t *testing.T) {
		c := &Cmd{Service: svc}
		c.resolveCommand()
		want := []string{"echo", "from-yaml"}
		if !reflect.DeepEqual(c.Args, want) {
			t.Fatalf("Args=%v want=%v", c.Args, want)
		}
	})

	t.Run("empty slice falls back", func(t *testing.T) {
		c := &Cmd{Service: svc, Args: []string{}}
		c.resolveCommand()
		want := []string{"echo", "from-yaml"}
		if !reflect.DeepEqual(c.Args, want) {
			t.Fatalf("Args=%v want=%v", c.Args, want)
		}
	})

	t.Run("explicit args are not overridden", func(t *testing.T) {
		c := &Cmd{Service: svc, Args: []string{"echo", "explicit"}}
		c.resolveCommand()
		want := []string{"echo", "explicit"}
		if !reflect.DeepEqual(c.Args, want) {
			t.Fatalf("Args=%v want=%v", c.Args, want)
		}
	})

	t.Run("empty-string arg is not treated as default", func(t *testing.T) {
		c := &Cmd{Service: svc, Args: []string{""}}
		c.resolveCommand()
		want := []string{""}
		if !reflect.DeepEqual(c.Args, want) {
			t.Fatalf("Args=%v want=%v", c.Args, want)
		}
	})
}

func TestWaitForExit_ClosedErrChStillWaitsForResp(t *testing.T) {
	respCh := make(chan container.WaitResponse)
	errCh := make(chan error)
	close(errCh)

	go func() {
		time.Sleep(50 * time.Millisecond)
		respCh <- container.WaitResponse{StatusCode: 0}
	}()

	start := time.Now()
	_, err := waitForExit(context.Background(), context.Background(), nil, "cid", respCh, errCh)
	if err != nil {
		t.Fatalf("waitForExit: %v", err)
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Fatalf("waitForExit returned before respCh was ready")
	}
}

func TestContainerConfigs_AddsComposeLabels(t *testing.T) {
	proj := &types.Project{Name: "proj"}
	svc := types.ServiceConfig{Name: "svc", Image: "alpine:latest"}
	s := NewService(proj, svc)

	c := &Cmd{Service: s.config, service: s}
	cfg, _ := c.containerConfigs(nil)
	if cfg.Labels == nil {
		t.Fatalf("labels nil")
	}
	if cfg.Labels["com.docker.compose.project"] != "proj" {
		t.Fatalf("project label=%q", cfg.Labels["com.docker.compose.project"])
	}
	if cfg.Labels["com.docker.compose.service"] != "svc" {
		t.Fatalf("service label=%q", cfg.Labels["com.docker.compose.service"])
	}
}
