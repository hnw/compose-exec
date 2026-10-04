package compose

import (
	"context"
	"errors"
	"io"
	"iter"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type fakeDocker struct {
	stopCalls   int
	stopErr     bool
	killCalls   int
	removeCalls int

	inspectResp container.InspectResponse
	inspectErr  error

	imageInspectErr error
	imagePullRef    string
	imagePullOpts   client.ImagePullOptions

	networkListResp    []network.Summary
	networkCreateCalls []networkCreateCall

	volumeCreateCalls []client.VolumeCreateOptions
}

type networkCreateCall struct {
	name    string
	options client.NetworkCreateOptions
}

func (f *fakeDocker) ImageInspect(
	_ context.Context,
	_ string,
	_ ...client.ImageInspectOption,
) (client.ImageInspectResult, error) {
	if f.imageInspectErr != nil {
		return client.ImageInspectResult{}, f.imageInspectErr
	}
	return client.ImageInspectResult{}, nil
}

func (f *fakeDocker) ImagePull(
	_ context.Context,
	ref string,
	options client.ImagePullOptions,
) (client.ImagePullResponse, error) {
	f.imagePullRef = ref
	f.imagePullOpts = options
	return fakePullResponse{ReadCloser: io.NopCloser(&nopReader{})}, nil
}

func (f *fakeDocker) ContainerCreate(
	_ context.Context,
	_ client.ContainerCreateOptions,
) (client.ContainerCreateResult, error) {
	return client.ContainerCreateResult{ID: "cid"}, nil
}

func (f *fakeDocker) ContainerStart(
	_ context.Context,
	_ string,
	_ client.ContainerStartOptions,
) (client.ContainerStartResult, error) {
	return client.ContainerStartResult{}, nil
}

func (f *fakeDocker) ContainerAttach(
	_ context.Context,
	_ string,
	_ client.ContainerAttachOptions,
) (client.ContainerAttachResult, error) {
	// Not used in unit tests.
	return client.ContainerAttachResult{}, nil
}

func (f *fakeDocker) ContainerWait(
	_ context.Context,
	_ string,
	_ client.ContainerWaitOptions,
) client.ContainerWaitResult {
	respCh := make(chan container.WaitResponse, 1)
	errCh := make(chan error, 1)
	respCh <- container.WaitResponse{StatusCode: 0}
	return client.ContainerWaitResult{Result: respCh, Error: errCh}
}

func (f *fakeDocker) ContainerInspect(
	_ context.Context,
	_ string,
	_ client.ContainerInspectOptions,
) (client.ContainerInspectResult, error) {
	if f.inspectErr != nil {
		return client.ContainerInspectResult{}, f.inspectErr
	}
	return client.ContainerInspectResult{Container: f.inspectResp}, nil
}

func (f *fakeDocker) ContainerStop(
	_ context.Context,
	_ string,
	_ client.ContainerStopOptions,
) (client.ContainerStopResult, error) {
	f.stopCalls++
	if f.stopErr {
		return client.ContainerStopResult{}, context.Canceled
	}
	return client.ContainerStopResult{}, nil
}

func (f *fakeDocker) ContainerKill(
	_ context.Context,
	_ string,
	_ client.ContainerKillOptions,
) (client.ContainerKillResult, error) {
	f.killCalls++
	return client.ContainerKillResult{}, nil
}

func (f *fakeDocker) ContainerRemove(
	_ context.Context,
	_ string,
	_ client.ContainerRemoveOptions,
) (client.ContainerRemoveResult, error) {
	f.removeCalls++
	return client.ContainerRemoveResult{}, nil
}

func (f *fakeDocker) ContainerList(
	_ context.Context,
	_ client.ContainerListOptions,
) (client.ContainerListResult, error) {
	return client.ContainerListResult{Items: []container.Summary{}}, nil
}

func (f *fakeDocker) NetworkList(
	_ context.Context,
	_ client.NetworkListOptions,
) (client.NetworkListResult, error) {
	return client.NetworkListResult{
		Items: append([]network.Summary(nil), f.networkListResp...),
	}, nil
}

func (f *fakeDocker) NetworkCreate(
	_ context.Context,
	name string,
	options client.NetworkCreateOptions,
) (client.NetworkCreateResult, error) {
	f.networkCreateCalls = append(f.networkCreateCalls, networkCreateCall{
		name:    name,
		options: options,
	})
	return client.NetworkCreateResult{ID: "fake-network-id"}, nil
}

func (f *fakeDocker) NetworkRemove(
	_ context.Context,
	_ string,
	_ client.NetworkRemoveOptions,
) (client.NetworkRemoveResult, error) {
	return client.NetworkRemoveResult{}, nil
}

func (f *fakeDocker) VolumeCreate(
	_ context.Context,
	options client.VolumeCreateOptions,
) (client.VolumeCreateResult, error) {
	f.volumeCreateCalls = append(f.volumeCreateCalls, options)
	return client.VolumeCreateResult{
		Volume: volume.Volume{Name: options.Name},
	}, nil
}

func (f *fakeDocker) Close() error {
	return nil
}

type nopReader struct{}

func (n *nopReader) Read(_ []byte) (int, error) { return 0, io.EOF }

// fakePullResponse implements client.ImagePullResponse for unit tests. Only the
// io.ReadCloser part is exercised by compose-exec; the streaming helpers are
// no-ops.
type fakePullResponse struct {
	io.ReadCloser
}

func (fakePullResponse) JSONMessages(
	context.Context,
) iter.Seq2[jsonstream.Message, error] {
	return func(func(jsonstream.Message, error) bool) {}
}

func (fakePullResponse) Wait(context.Context) error { return nil }

func TestCmdContainerConfigsTTY(t *testing.T) {
	cmd := &Cmd{
		TTY:     true,
		Service: types.ServiceConfig{Image: "busybox:1.36"},
	}
	cfg, _, err := cmd.containerConfigs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Tty {
		t.Fatal("container TTY is disabled")
	}
}

type testEnvValue struct {
	value    string
	hasValue bool
}

func parseEnvSlice(ss []string) map[string]testEnvValue {
	out := make(map[string]testEnvValue, len(ss))
	for _, kv := range ss {
		k, v, ok := splitEnv(kv)
		if ok {
			out[k] = testEnvValue{value: v, hasValue: true}
			continue
		}
		out[kv] = testEnvValue{value: "", hasValue: false}
	}
	return out
}

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

func TestCmd_Environ_MergeAndCopy(t *testing.T) {
	v1 := "1"
	v2 := "2"
	svc := types.ServiceConfig{
		Environment: types.MappingWithEquals{
			"A": &v1,
			"B": &v2,
		},
	}
	c := &Cmd{
		Service: svc,
		Env:     []string{"B=20", "C=3"},
	}

	got := c.Environ()
	gotMap := parseEnvSlice(got)

	if ev, ok := gotMap["A"]; !ok || !ev.hasValue || ev.value != "1" {
		t.Fatalf("env A=%v ok=%v", ev, ok)
	}
	if ev, ok := gotMap["B"]; !ok || !ev.hasValue || ev.value != "20" {
		t.Fatalf("env B=%v ok=%v", ev, ok)
	}
	if ev, ok := gotMap["C"]; !ok || !ev.hasValue || ev.value != "3" {
		t.Fatalf("env C=%v ok=%v", ev, ok)
	}

	got[0] = "Z=9"
	if c.Env[0] != "B=20" {
		t.Fatalf("Env mutated: %v", c.Env)
	}
}

func TestCmd_StdoutPipe_Errors(t *testing.T) {
	t.Run("already started", func(t *testing.T) {
		c := &Cmd{}
		_ = c.markStarted()
		if _, err := c.StdoutPipe(); err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("stdout set", func(t *testing.T) {
		c := &Cmd{Stdout: io.Discard}
		if _, err := c.StdoutPipe(); err == nil {
			t.Fatalf("expected error")
		}
	})
}

func TestCmd_StderrPipe_Errors(t *testing.T) {
	t.Run("already started", func(t *testing.T) {
		c := &Cmd{}
		_ = c.markStarted()
		if _, err := c.StderrPipe(); err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("stderr set", func(t *testing.T) {
		c := &Cmd{Stderr: io.Discard}
		if _, err := c.StderrPipe(); err == nil {
			t.Fatalf("expected error")
		}
	})
}

func TestCmd_StdinPipe_Errors(t *testing.T) {
	t.Run("already started", func(t *testing.T) {
		c := &Cmd{}
		_ = c.markStarted()
		if _, err := c.StdinPipe(); err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("stdin set", func(t *testing.T) {
		c := &Cmd{Stdin: io.NopCloser(&nopReader{})}
		if _, err := c.StdinPipe(); err == nil {
			t.Fatalf("expected error")
		}
	})
}

func TestCmd_Pipes_CloseBehavior(t *testing.T) {
	t.Run("stdout pipe closes", func(t *testing.T) {
		c := &Cmd{}
		r, err := c.StdoutPipe()
		if err != nil {
			t.Fatalf("StdoutPipe: %v", err)
		}
		c.closeStdoutPipe(nil)
		buf := make([]byte, 1)
		n, err := r.Read(buf)
		if n != 0 || err != io.EOF {
			t.Fatalf("read n=%d err=%v", n, err)
		}
	})

	t.Run("stderr pipe closes", func(t *testing.T) {
		c := &Cmd{}
		r, err := c.StderrPipe()
		if err != nil {
			t.Fatalf("StderrPipe: %v", err)
		}
		c.closeStderrPipe(nil)
		buf := make([]byte, 1)
		n, err := r.Read(buf)
		if n != 0 || err != io.EOF {
			t.Fatalf("read n=%d err=%v", n, err)
		}
	})

	t.Run("stdin pipe closes", func(t *testing.T) {
		c := &Cmd{}
		w, err := c.StdinPipe()
		if err != nil {
			t.Fatalf("StdinPipe: %v", err)
		}
		c.closeStdinPipe(nil)
		if _, err := w.Write([]byte("x")); err == nil {
			t.Fatalf("expected write error")
		}
		_ = w.Close()
	})
}

func TestStdinEnabled(t *testing.T) {
	t.Run("empty strings.Reader", func(t *testing.T) {
		if stdinEnabled(strings.NewReader("")) {
			t.Fatalf("expected stdinEnabled to be false")
		}
	})

	t.Run("non-empty strings.Reader", func(t *testing.T) {
		if !stdinEnabled(strings.NewReader("x")) {
			t.Fatalf("expected stdinEnabled to be true")
		}
	})
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

	mounts, err := serviceMounts(svc, "/tmp/project", "proj", nil)
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

	mounts, err := serviceMounts(svc, "/tmp/project", "myproj", nil)
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

func TestServiceMounts_NamedVolume_UsesTopLevelCustomName(t *testing.T) {
	svc := types.ServiceConfig{
		Volumes: []types.ServiceVolumeConfig{{
			Type:   types.VolumeTypeVolume,
			Source: "db_data",
			Target: "/data",
		}},
	}
	projectVolumes := types.Volumes{
		"db_data": types.VolumeConfig{Name: "custom_data"},
	}

	mounts, err := serviceMounts(svc, "/tmp/project", "myproj", projectVolumes)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(mounts) != 1 {
		t.Fatalf("mounts=%d", len(mounts))
	}
	if mounts[0].Source != "custom_data" {
		t.Fatalf("source=%q want=%q", mounts[0].Source, "custom_data")
	}
}

func TestServiceMounts_NamedVolume_UsesExternalVolumeName(t *testing.T) {
	svc := types.ServiceConfig{
		Volumes: []types.ServiceVolumeConfig{{
			Type:   types.VolumeTypeVolume,
			Source: "shared",
			Target: "/data",
		}},
	}
	projectVolumes := types.Volumes{
		"shared": types.VolumeConfig{
			External: types.External(true),
		},
	}

	mounts, err := serviceMounts(svc, "/tmp/project", "myproj", projectVolumes)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(mounts) != 1 {
		t.Fatalf("mounts=%d", len(mounts))
	}
	if mounts[0].Source != "shared" {
		t.Fatalf("source=%q want=%q", mounts[0].Source, "shared")
	}
}

func TestServiceMounts_TmpfsVolume(t *testing.T) {
	svc := types.ServiceConfig{
		Volumes: []types.ServiceVolumeConfig{{
			Type:     types.VolumeTypeTmpfs,
			Target:   "/run",
			ReadOnly: true,
			Tmpfs: &types.ServiceVolumeTmpfs{
				Size: 64 * 1024,
				Mode: 0o1777,
			},
		}},
	}

	mounts, err := serviceMounts(svc, "/tmp/project", "proj", nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(mounts) != 1 {
		t.Fatalf("mounts=%d", len(mounts))
	}
	if mounts[0].Type != mount.TypeTmpfs {
		t.Fatalf("type=%q want=%q", mounts[0].Type, mount.TypeTmpfs)
	}
	if mounts[0].Target != "/run" {
		t.Fatalf("target=%q", mounts[0].Target)
	}
	if !mounts[0].ReadOnly {
		t.Fatalf("ReadOnly=false want=true")
	}
	if mounts[0].TmpfsOptions == nil {
		t.Fatalf("TmpfsOptions nil")
	}
	if mounts[0].TmpfsOptions.SizeBytes != 64*1024 {
		t.Fatalf("SizeBytes=%d want=%d", mounts[0].TmpfsOptions.SizeBytes, int64(64*1024))
	}
	if mounts[0].TmpfsOptions.Mode != os.FileMode(0o1777) {
		t.Fatalf("Mode=%v want=%v", mounts[0].TmpfsOptions.Mode, os.FileMode(0o1777))
	}
}

func TestCmd_ensureVolumes_CreatesTopLevelProjectVolumes(t *testing.T) {
	fd := &fakeDocker{}

	svcCfg := types.ServiceConfig{
		Name:  "alpine",
		Image: "alpine:latest",
		Volumes: []types.ServiceVolumeConfig{
			{Type: types.VolumeTypeVolume, Source: "db_data"},
		},
	}
	proj := &Project{
		Name: "myproj",
		Volumes: types.Volumes{
			"db_data": types.VolumeConfig{},
		},
		Services: types.Services{"alpine": svcCfg},
	}

	s, err := proj.Service("alpine")
	if err != nil {
		t.Fatalf("Project.Service: %v", err)
	}

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

func TestCmd_ensureVolumes_RespectsTopLevelNameAndExternal(t *testing.T) {
	fd := &fakeDocker{}

	svcCfg := types.ServiceConfig{
		Name:  "alpine",
		Image: "alpine:latest",
		Volumes: []types.ServiceVolumeConfig{
			{Type: types.VolumeTypeVolume, Source: "managed"},
			{Type: types.VolumeTypeVolume, Source: "plain"},
			{Type: types.VolumeTypeVolume, Source: "shared"},
		},
	}
	proj := &Project{
		Name: "myproj",
		Volumes: types.Volumes{
			"managed": types.VolumeConfig{Name: "custom_managed"},
			"plain":   types.VolumeConfig{},
			"shared": types.VolumeConfig{
				Name:     "corp_shared",
				External: types.External(true),
			},
		},
		Services: types.Services{"alpine": svcCfg},
	}
	s, err := proj.Service("alpine")
	if err != nil {
		t.Fatalf("Project.Service: %v", err)
	}

	c := &Cmd{Service: s.config, service: s}
	if err := c.ensureVolumes(context.Background(), fd); err != nil {
		t.Fatalf("ensureVolumes: %v", err)
	}

	if len(fd.volumeCreateCalls) != 2 {
		t.Fatalf("calls=%d want=2", len(fd.volumeCreateCalls))
	}
	got := map[string]client.VolumeCreateOptions{}
	for _, call := range fd.volumeCreateCalls {
		got[call.Name] = call
	}
	if _, ok := got["corp_shared"]; ok {
		t.Fatalf("external volume must not be created: %#v", got["corp_shared"])
	}
	if _, ok := got["custom_managed"]; !ok {
		t.Fatalf("custom named volume not created: calls=%v", fd.volumeCreateCalls)
	}
	if _, ok := got["myproj_plain"]; !ok {
		t.Fatalf("default project-prefixed volume not created: calls=%v", fd.volumeCreateCalls)
	}
}

func TestCmd_ensureNetworks_RespectsTopLevelNameAndExternal(t *testing.T) {
	fd := &fakeDocker{}

	svcCfg := types.ServiceConfig{
		Name:  "svc",
		Image: "alpine:latest",
		Networks: map[string]*types.ServiceNetworkConfig{
			"app": nil,
			"shared": {
				Aliases: []string{"shared-alias"},
			},
		},
	}
	proj := &Project{
		Name: "myproj",
		Networks: types.Networks{
			"app": types.NetworkConfig{Name: "custom_app_net"},
			"shared": types.NetworkConfig{
				Name:     "corp_shared_net",
				External: types.External(true),
			},
		},
		Services: types.Services{"svc": svcCfg},
	}
	s, err := proj.Service("svc")
	if err != nil {
		t.Fatalf("Project.Service: %v", err)
	}
	c := &Cmd{Service: s.config, service: s}

	plan, err := c.resolveNetworking(context.Background(), fd)
	if err != nil {
		t.Fatalf("resolveNetworking: %v", err)
	}
	if plan == nil || plan.config == nil {
		t.Fatalf("resolveNetworking returned nil")
	}
	if _, ok := plan.config.EndpointsConfig["custom_app_net"]; !ok {
		t.Fatalf("missing endpoint for custom_app_net: %v", plan.config.EndpointsConfig)
	}
	if _, ok := plan.config.EndpointsConfig["corp_shared_net"]; !ok {
		t.Fatalf("missing endpoint for corp_shared_net: %v", plan.config.EndpointsConfig)
	}

	if err := c.ensureNetworks(context.Background(), fd, plan); err != nil {
		t.Fatalf("ensureNetworks: %v", err)
	}

	if len(fd.networkCreateCalls) != 1 {
		t.Fatalf("NetworkCreate calls=%d want=1", len(fd.networkCreateCalls))
	}
	call := fd.networkCreateCalls[0]
	if call.name != "custom_app_net" {
		t.Fatalf("created network=%q want=%q", call.name, "custom_app_net")
	}
	if call.options.Labels["com.docker.compose.network"] != "app" {
		t.Fatalf(
			"network label=%q want=%q",
			call.options.Labels["com.docker.compose.network"],
			"app",
		)
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

func TestCmd_WaitUntilHealthy_StopsOnSignalContext(t *testing.T) {
	fd := &fakeDocker{
		inspectResp: container.InspectResponse{
			State: &container.State{
				Running: true,
				Health: &container.Health{
					Status: "starting",
				},
			},
		},
	}

	ctx, cancelCtx := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelCtx()
	sigCtx, cancelSig := context.WithCancel(context.Background())
	defer cancelSig()

	c := &Cmd{
		Service: types.ServiceConfig{
			Name:  "svc",
			Image: "alpine:latest",
			HealthCheck: &types.HealthCheckConfig{
				Test: []string{"CMD", "true"},
			},
		},
		ctx:         ctx,
		docker:      fd,
		started:     true,
		containerID: "cid",
		waitRespCh:  make(chan container.WaitResponse),
		signalCtx:   sigCtx,
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancelSig()
	}()

	start := time.Now()
	err := c.WaitUntilHealthy()
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want=%v", err, context.Canceled)
	}
	if elapsed > 1200*time.Millisecond {
		t.Fatalf("WaitUntilHealthy did not stop quickly on signal context: %v", elapsed)
	}
}

func TestContainerConfigs_AddsComposeLabels(t *testing.T) {
	svc := types.ServiceConfig{Name: "svc", Image: "alpine:latest"}
	proj := &Project{Name: "proj", Services: types.Services{"svc": svc}}
	s, err := proj.Service("svc")
	if err != nil {
		t.Fatalf("Project.Service: %v", err)
	}

	c := &Cmd{Service: s.config, service: s}
	cfg, _, err := c.containerConfigs(nil)
	if err != nil {
		t.Fatalf("containerConfigs: %v", err)
	}
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

func TestContainerConfigs_WorkingDirOverride(t *testing.T) {
	svc := types.ServiceConfig{
		Image:      "alpine:latest",
		WorkingDir: "/service",
	}
	c := &Cmd{
		Service:    svc,
		WorkingDir: "/override",
	}
	cfg, _, err := c.containerConfigs(nil)
	if err != nil {
		t.Fatalf("containerConfigs: %v", err)
	}
	if cfg.WorkingDir != "/override" {
		t.Fatalf("WorkingDir=%q want=%q", cfg.WorkingDir, "/override")
	}
}

func TestContainerConfigs_ReadOnlyRootfs(t *testing.T) {
	svc := types.ServiceConfig{
		Image:    "alpine:latest",
		ReadOnly: true,
	}
	c := &Cmd{Service: svc}

	_, hostCfg, err := c.containerConfigs(nil)
	if err != nil {
		t.Fatalf("containerConfigs: %v", err)
	}
	if !hostCfg.ReadonlyRootfs {
		t.Fatalf("ReadonlyRootfs=false want=true")
	}
}

func TestContainerConfigs_TmpfsMapping(t *testing.T) {
	svc := types.ServiceConfig{
		Image: "alpine:latest",
		Tmpfs: types.StringList{
			"/run:size=64m,mode=1777,noexec",
			"/cache",
		},
	}
	c := &Cmd{Service: svc}

	_, hostCfg, err := c.containerConfigs(nil)
	if err != nil {
		t.Fatalf("containerConfigs: %v", err)
	}
	want := map[string]string{
		"/run":   "size=64m,mode=1777,noexec",
		"/cache": "",
	}
	if !reflect.DeepEqual(hostCfg.Tmpfs, want) {
		t.Fatalf("Tmpfs=%v want=%v", hostCfg.Tmpfs, want)
	}
}

func TestContainerConfigs_MapsAdditionalHostOptions(t *testing.T) {
	svc := types.ServiceConfig{
		Image:       "alpine:latest",
		SecurityOpt: []string{"no-new-privileges:true", "label=disable"},
		ShmSize:     types.UnitBytes(128 * 1024 * 1024),
		ExtraHosts: types.HostsList{
			"example.local": []string{"127.0.0.1"},
			"api.local":     []string{"10.0.0.10", "10.0.0.11"},
		},
		Devices: []types.DeviceMapping{
			{
				Source:      "/dev/null",
				Target:      "/dev/xnull",
				Permissions: "r",
			},
			{
				Source: "/dev/zero",
			},
		},
		CPUS:      1.5,
		CPUShares: 512,
		CPUSet:    "0,2",
	}
	c := &Cmd{Service: svc}

	_, hostCfg, err := c.containerConfigs(nil)
	if err != nil {
		t.Fatalf("containerConfigs: %v", err)
	}

	if !reflect.DeepEqual(hostCfg.SecurityOpt, svc.SecurityOpt) {
		t.Fatalf("SecurityOpt=%v want=%v", hostCfg.SecurityOpt, svc.SecurityOpt)
	}
	if hostCfg.ShmSize != int64(svc.ShmSize) {
		t.Fatalf("ShmSize=%d want=%d", hostCfg.ShmSize, int64(svc.ShmSize))
	}

	wantHosts := svc.ExtraHosts.AsList(":")
	if !sameStringMultiset(hostCfg.ExtraHosts, wantHosts) {
		t.Fatalf("ExtraHosts=%v want(as set)=%v", hostCfg.ExtraHosts, wantHosts)
	}

	wantDevices := []container.DeviceMapping{
		{
			PathOnHost:        "/dev/null",
			PathInContainer:   "/dev/xnull",
			CgroupPermissions: "r",
		},
		{
			PathOnHost:        "/dev/zero",
			PathInContainer:   "/dev/zero",
			CgroupPermissions: "rwm",
		},
	}
	if !reflect.DeepEqual(hostCfg.Devices, wantDevices) {
		t.Fatalf("Devices=%v want=%v", hostCfg.Devices, wantDevices)
	}

	if hostCfg.NanoCPUs != 1_500_000_000 {
		t.Fatalf("NanoCPUs=%d want=%d", hostCfg.NanoCPUs, int64(1_500_000_000))
	}
	if hostCfg.CPUShares != 512 {
		t.Fatalf("CPUShares=%d want=%d", hostCfg.CPUShares, int64(512))
	}
	if hostCfg.CpusetCpus != "0,2" {
		t.Fatalf("CpusetCpus=%q want=%q", hostCfg.CpusetCpus, "0,2")
	}
}

func TestContainerConfigs_LoadsSeccompProfileFromFile(t *testing.T) {
	dir := t.TempDir()
	profile := `{"defaultAction":"SCMP_ACT_ERRNO"}`
	if err := os.WriteFile(filepath.Join(dir, "seccomp.json"), []byte(profile), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	svc := types.ServiceConfig{
		Image:       "alpine:latest",
		SecurityOpt: []string{"seccomp:seccomp.json"},
	}
	project := &Project{WorkingDir: dir}
	s := newService(project, svc)
	c := &Cmd{Service: s.config, service: s}

	_, hostCfg, err := c.containerConfigs(nil)
	if err != nil {
		t.Fatalf("containerConfigs: %v", err)
	}
	if len(hostCfg.SecurityOpt) != 1 {
		t.Fatalf("SecurityOpt len=%d want=1", len(hostCfg.SecurityOpt))
	}
	want := "seccomp=" + profile
	if hostCfg.SecurityOpt[0] != want {
		t.Fatalf("SecurityOpt=%q want=%q", hostCfg.SecurityOpt[0], want)
	}
}

func sameStringMultiset(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	counts := map[string]int{}
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
	}
	for _, n := range counts {
		if n != 0 {
			return false
		}
	}
	return true
}

func TestPullImage(t *testing.T) {
	t.Run("existing image is not pulled", func(t *testing.T) {
		fd := &fakeDocker{}
		if err := pullImage(context.Background(), fd, "alpine:latest", "linux/amd64"); err != nil {
			t.Fatalf("pullImage: %v", err)
		}
		if fd.imagePullRef != "" {
			t.Fatalf("ImagePull called for ref=%q", fd.imagePullRef)
		}
	})

	t.Run("inspect error other than not-found is propagated", func(t *testing.T) {
		inspectErr := errors.New("boom")
		fd := &fakeDocker{imageInspectErr: inspectErr}
		if err := pullImage(context.Background(), fd, "alpine:latest", ""); !errors.Is(
			err,
			inspectErr,
		) {
			t.Fatalf("err=%v want=%v", err, inspectErr)
		}
	})

	t.Run("platform is forwarded as an OCI platform", func(t *testing.T) {
		fd := &fakeDocker{imageInspectErr: cerrdefs.ErrNotFound}
		if err := pullImage(context.Background(), fd, "alpine:latest", "linux/arm/v7"); err != nil {
			t.Fatalf("pullImage: %v", err)
		}
		if fd.imagePullRef != "alpine:latest" {
			t.Fatalf("pulled ref=%q want=%q", fd.imagePullRef, "alpine:latest")
		}
		want := []ocispec.Platform{{OS: "linux", Architecture: "arm", Variant: "v7"}}
		if !reflect.DeepEqual(fd.imagePullOpts.Platforms, want) {
			t.Fatalf("Platforms=%v want=%v", fd.imagePullOpts.Platforms, want)
		}
	})

	t.Run("no platform leaves Platforms empty", func(t *testing.T) {
		fd := &fakeDocker{imageInspectErr: cerrdefs.ErrNotFound}
		if err := pullImage(context.Background(), fd, "alpine:latest", ""); err != nil {
			t.Fatalf("pullImage: %v", err)
		}
		if len(fd.imagePullOpts.Platforms) != 0 {
			t.Fatalf("Platforms=%v want empty", fd.imagePullOpts.Platforms)
		}
	})
}

func TestCmd_ServicePorts_Mapping(t *testing.T) {
	svc := types.ServiceConfig{
		Ports: []types.ServicePortConfig{
			{Target: 80, Published: "8080", HostIP: "127.0.0.1", Protocol: "tcp"},
			{Target: 53, Published: "5353"},
		},
	}
	c := &Cmd{Service: svc}

	exposed, bindings, err := c.servicePorts()
	if err != nil {
		t.Fatalf("servicePorts: %v", err)
	}

	wantExposed := network.PortSet{
		network.MustParsePort("80/tcp"): {},
		network.MustParsePort("53/tcp"): {},
	}
	if !reflect.DeepEqual(exposed, wantExposed) {
		t.Fatalf("ExposedPorts=%v want=%v", exposed, wantExposed)
	}

	hostIP := netip.MustParseAddr("127.0.0.1")
	wantBindings := network.PortMap{
		network.MustParsePort("80/tcp"): {
			{HostIP: hostIP, HostPort: "8080"},
		},
		network.MustParsePort("53/tcp"): {
			{HostPort: "5353"},
		},
	}
	if !reflect.DeepEqual(bindings, wantBindings) {
		t.Fatalf("PortBindings=%v want=%v", bindings, wantBindings)
	}
}

func TestCmd_ServicePorts_InvalidValues(t *testing.T) {
	t.Run("invalid host_ip", func(t *testing.T) {
		c := &Cmd{Service: types.ServiceConfig{Ports: []types.ServicePortConfig{
			{Target: 80, Published: "8080", HostIP: "not-an-ip"},
		}}}
		if _, _, err := c.servicePorts(); err == nil {
			t.Fatal("servicePorts() error = nil, want error")
		}
	})

	t.Run("target out of range", func(t *testing.T) {
		c := &Cmd{Service: types.ServiceConfig{Ports: []types.ServicePortConfig{
			{Target: 70000},
		}}}
		if _, _, err := c.servicePorts(); err == nil {
			t.Fatal("servicePorts() error = nil, want error")
		}
	})
}

func TestEndpointSettings_NetworkTypes(t *testing.T) {
	cfg := &types.ServiceNetworkConfig{
		Ipv4Address:  "10.0.0.7",
		Ipv6Address:  "fd00::7",
		LinkLocalIPs: []string{"169.254.1.1"},
		MacAddress:   "02:42:ac:11:00:02",
	}

	settings, err := endpointSettings("svc", cfg)
	if err != nil {
		t.Fatalf("endpointSettings: %v", err)
	}
	if settings.IPAMConfig == nil {
		t.Fatal("IPAMConfig is nil")
	}
	if got := settings.IPAMConfig.IPv4Address; got != netip.MustParseAddr("10.0.0.7") {
		t.Fatalf("IPv4Address=%v", got)
	}
	if got := settings.IPAMConfig.IPv6Address; got != netip.MustParseAddr("fd00::7") {
		t.Fatalf("IPv6Address=%v", got)
	}
	want := []netip.Addr{netip.MustParseAddr("169.254.1.1")}
	if !reflect.DeepEqual(settings.IPAMConfig.LinkLocalIPs, want) {
		t.Fatalf("LinkLocalIPs=%v want=%v", settings.IPAMConfig.LinkLocalIPs, want)
	}
	if got, want := settings.MacAddress.String(), "02:42:ac:11:00:02"; got != want {
		t.Fatalf("MacAddress=%q want=%q", got, want)
	}

	t.Run("invalid values are rejected", func(t *testing.T) {
		for name, cfg := range map[string]*types.ServiceNetworkConfig{
			"ipv4_address":   {Ipv4Address: "10.0.0.999"},
			"ipv6_address":   {Ipv6Address: "fd00::zz"},
			"link_local_ips": {LinkLocalIPs: []string{"nope"}},
			"mac_address":    {MacAddress: "zz:zz"},
		} {
			if _, err := endpointSettings("svc", cfg); err == nil {
				t.Errorf("endpointSettings(%s) error = nil, want error", name)
			}
		}
	})
}

func TestDockerIPAMConfig_NetworkTypes(t *testing.T) {
	ipam, err := dockerIPAMConfig(types.IPAMConfig{
		Driver: "default",
		Config: []*types.IPAMPool{{
			Subnet:             "10.1.0.0/16",
			IPRange:            "10.1.1.0/24",
			Gateway:            "10.1.0.1",
			AuxiliaryAddresses: map[string]string{"reserved": "10.1.0.2"},
		}},
	})
	if err != nil {
		t.Fatalf("dockerIPAMConfig: %v", err)
	}
	if ipam.Driver != "default" || len(ipam.Config) != 1 {
		t.Fatalf("ipam=%+v", ipam)
	}
	cfg := ipam.Config[0]
	if got := cfg.Subnet.String(); got != "10.1.0.0/16" {
		t.Fatalf("Subnet=%q want=%q", got, "10.1.0.0/16")
	}
	if got := cfg.IPRange.String(); got != "10.1.1.0/24" {
		t.Fatalf("IPRange=%q want=%q", got, "10.1.1.0/24")
	}
	if got := cfg.Gateway.String(); got != "10.1.0.1" {
		t.Fatalf("Gateway=%q want=%q", got, "10.1.0.1")
	}
	if got := cfg.AuxAddress["reserved"].String(); got != "10.1.0.2" {
		t.Fatalf("AuxAddress=%q want=%q", got, "10.1.0.2")
	}

	t.Run("invalid values are rejected", func(t *testing.T) {
		for name, cfg := range map[string]types.IPAMConfig{
			"subnet":   {Config: []*types.IPAMPool{{Subnet: "10.1.0.0/64"}}},
			"ip_range": {Config: []*types.IPAMPool{{IPRange: "nope"}}},
			"gateway":  {Config: []*types.IPAMPool{{Gateway: "nope"}}},
			"auxiliary_addresses": {Config: []*types.IPAMPool{{
				AuxiliaryAddresses: map[string]string{"a": "nope"},
			}}},
		} {
			if _, err := dockerIPAMConfig(cfg); err == nil {
				t.Errorf("dockerIPAMConfig(%s) error = nil, want error", name)
			}
		}
	})
}
