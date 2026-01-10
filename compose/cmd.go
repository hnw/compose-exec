// Package compose provides a small API to execute commands in Compose services via Docker Engine.
package compose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	cerrdefs "github.com/containerd/errdefs"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/pkg/stdcopy"
)

// Cmd represents a pending command execution, similar to os/exec.Cmd.
type Cmd struct {
	// Public fields
	Service types.ServiceConfig
	Path    string
	Args    []string
	Env     []string

	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// Delayed error propagated from Service initialization.
	loadErr error

	// Internal
	service *Service
	docker  dockerAPI

	mu          sync.Mutex
	started     bool
	containerID string
	waitRespCh  <-chan container.WaitResponse
	waitErrCh   <-chan error
	attach      *dockertypes.HijackedResponse
	ioDone      chan struct{}
	stdinDone   chan struct{}
	signalCtx   context.Context
	signalStop  func()

	stderrBuf bytes.Buffer
}

// Run starts the container and waits for it to exit, similar to (*exec.Cmd).Run.
func (c *Cmd) Run(ctx context.Context) error {
	if c.loadErr != nil {
		return c.loadErr
	}
	if err := c.Start(ctx); err != nil {
		return err
	}
	return c.Wait(ctx)
}

func (c *Cmd) markStarted() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return errors.New("compose: already started")
	}
	c.started = true
	return nil
}

func (c *Cmd) ensureService() {
	if c.service == nil {
		// Allow constructing Cmd manually, but then working dir resolution uses process cwd.
		c.service = NewService(c.Service)
	}
}

func (c *Cmd) resolveCommand() {
	// Command resolution priority:
	// 1) Explicit args/path
	// 2) Service.Command from YAML
	// 3) Delegate to image defaults (no error)
	if len(c.Args) == 0 && c.Path != "" {
		c.Args = []string{c.Path}
	}
	if len(c.Args) == 0 && len(c.Service.Command) > 0 {
		c.Args = []string(c.Service.Command)
	}
	if c.Path == "" && len(c.Args) > 0 {
		c.Path = c.Args[0]
	}
}

func (c *Cmd) storeSignal(sigCtx context.Context, stopSignals func()) {
	c.mu.Lock()
	c.signalCtx = sigCtx
	c.signalStop = stopSignals
	c.mu.Unlock()
}

func (c *Cmd) ensureDockerClient() (dockerAPI, error) {
	dc := c.docker
	if dc != nil {
		return dc, nil
	}
	cli, err := newDockerClient()
	if err != nil {
		return nil, err
	}
	c.docker = cli
	return cli, nil
}

func (c *Cmd) normalizedWriters() (io.Writer, io.Writer) {
	stdout := c.Stdout
	stderr := c.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	// Always capture stderr (bounded by memory) to surface on ExitError.
	stderr = io.MultiWriter(stderr, &c.stderrBuf)
	return stdout, stderr
}

func (c *Cmd) containerConfigs(mounts []mount.Mount) (*container.Config, *container.HostConfig) {
	// Respect docker-compose.yml as the source of truth for user.
	// If empty, omit and let Docker Engine/image defaults apply.
	user := strings.TrimSpace(c.Service.User)

	initEnabled := true
	if c.Service.Init != nil {
		initEnabled = *c.Service.Init
	}

	envBase := serviceEnvSlice(c.Service)
	env := mergeEnv(envBase, c.Env)

	cfg := &container.Config{
		Image:      c.Service.Image,
		WorkingDir: c.Service.WorkingDir,
		Env:        env,
		// TODO: Future support for TTY (out of scope).
		Tty:       false,
		OpenStdin: c.Stdin != nil,
		StdinOnce: false,
	}
	if hc := c.Service.HealthCheck; hc != nil {
		cfg.Healthcheck = dockerHealthConfig(hc)
	}
	if user != "" {
		cfg.User = user
	}
	if len(c.Args) > 0 {
		cfg.Cmd = c.Args
	}
	if len(c.Service.Entrypoint) > 0 {
		cfg.Entrypoint = []string(c.Service.Entrypoint)
	}

	hostCfg := &container.HostConfig{
		Init:   ptr(initEnabled),
		Mounts: mounts,
	}
	applyHostSecurityConfig(hostCfg, c.Service)
	if nm := strings.TrimSpace(c.Service.NetworkMode); nm != "" {
		hostCfg.NetworkMode = container.NetworkMode(nm)
	}
	return cfg, hostCfg
}

func dockerHealthConfig(hc *types.HealthCheckConfig) *container.HealthConfig {
	dockerHC := &container.HealthConfig{}
	if hc.Disable {
		dockerHC.Test = []string{"NONE"}
		return dockerHC
	}

	dockerHC.Test = []string(hc.Test)
	if hc.Interval != nil {
		dockerHC.Interval = time.Duration(*hc.Interval)
	}
	if hc.Timeout != nil {
		dockerHC.Timeout = time.Duration(*hc.Timeout)
	}
	if hc.StartPeriod != nil {
		dockerHC.StartPeriod = time.Duration(*hc.StartPeriod)
	}
	if hc.StartInterval != nil {
		dockerHC.StartInterval = time.Duration(*hc.StartInterval)
	}
	if hc.Retries != nil {
		retries := *hc.Retries
		if retries > uint64(math.MaxInt) {
			dockerHC.Retries = math.MaxInt
		} else {
			dockerHC.Retries = int(retries)
		}
	}
	return dockerHC
}

func applyHostSecurityConfig(hostCfg *container.HostConfig, svc types.ServiceConfig) {
	if hostCfg == nil {
		return
	}
	hostCfg.Privileged = svc.Privileged
	if len(svc.CapAdd) > 0 {
		hostCfg.CapAdd = append(hostCfg.CapAdd, svc.CapAdd...)
	}
	if len(svc.CapDrop) > 0 {
		hostCfg.CapDrop = append(hostCfg.CapDrop, svc.CapDrop...)
	}
}

// WaitUntilHealthy blocks until the started container becomes healthy.
//
// Strict behavior:
// - If the service has no healthcheck defined, it returns an error immediately.
// - If the container becomes unhealthy or stops running, it returns an error immediately.
func (c *Cmd) WaitUntilHealthy(ctx context.Context) error {
	if c.loadErr != nil {
		return c.loadErr
	}
	if ctx == nil {
		return errors.New("compose: ctx is required")
	}
	if c.Service.HealthCheck == nil {
		return errors.New("compose: healthcheck is not defined for this service")
	}

	st, err := c.snapshotWaitState()
	if err != nil {
		return err
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		status, err := inspectHealthStatus(ctx, st.dc, st.id)
		if err != nil {
			return err
		}
		if status == healthStatusHealthy {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type healthStatus int

const (
	healthStatusPending healthStatus = iota
	healthStatusHealthy
)

func inspectHealthStatus(
	ctx context.Context,
	dc dockerAPI,
	containerID string,
) (healthStatus, error) {
	j, err := dc.ContainerInspect(ctx, containerID)
	if err != nil {
		return healthStatusPending, err
	}
	if j.State == nil {
		return healthStatusPending, errors.New("compose: container state unavailable")
	}
	if !j.State.Running {
		return healthStatusPending, fmt.Errorf(
			"compose: container stopped (status=%s)",
			j.State.Status,
		)
	}
	if j.State.Health == nil {
		return healthStatusPending, errors.New("compose: container has no healthcheck")
	}
	switch j.State.Health.Status {
	case "healthy":
		return healthStatusHealthy, nil
	case "unhealthy":
		return healthStatusPending, errors.New("compose: container became unhealthy")
	default:
		return healthStatusPending, nil
	}
}

func (c *Cmd) storeContainerID(id string) {
	c.mu.Lock()
	c.containerID = id
	c.mu.Unlock()
}

func (c *Cmd) storeAttachState(attachResp *dockertypes.HijackedResponse) {
	c.mu.Lock()
	c.attach = attachResp
	c.ioDone = make(chan struct{})
	c.stdinDone = make(chan struct{})
	c.mu.Unlock()
}

func (c *Cmd) startForwarding(attachResp dockertypes.HijackedResponse, stdout, stderr io.Writer) {
	ioDone := c.ioDone
	stdinDone := c.stdinDone

	go func() {
		defer close(ioDone)
		_, _ = stdcopy.StdCopy(stdout, stderr, attachResp.Reader)
	}()

	go func() {
		defer close(stdinDone)
		if c.Stdin == nil {
			return
		}
		_, _ = io.Copy(attachResp.Conn, c.Stdin)
		_ = attachResp.CloseWrite()
	}()
}

func (c *Cmd) storeWait(dc dockerAPI, id string) {
	// NOTE: Do not use sigCtx for ContainerWait; if sigCtx is canceled by a signal,
	// Docker may return a context-canceled error instead of letting us stop the container.
	respCh, errCh := dc.ContainerWait(
		context.Background(),
		id,
		container.WaitConditionNotRunning,
	)
	c.mu.Lock()
	c.waitRespCh = respCh
	c.waitErrCh = errCh
	c.mu.Unlock()
}

// Start creates and starts the container for the configured service command.
func (c *Cmd) Start(ctx context.Context) (startErr error) {
	if c.loadErr != nil {
		return c.loadErr
	}
	if err := c.markStarted(); err != nil {
		return err
	}

	if ctx == nil {
		return errors.New("compose: ctx is required")
	}
	c.ensureService()
	c.resolveCommand()
	if c.Service.Image == "" {
		return errors.New("compose: service.image is required (build is out of scope)")
	}

	// Signal handling (Ctrl+C etc.) is handled internally per SOW.
	sigCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer func() {
		if startErr != nil && stopSignals != nil {
			stopSignals()
		}
	}()
	c.storeSignal(sigCtx, stopSignals)

	dc, err := c.ensureDockerClient()
	if err != nil {
		return err
	}

	// Pull image (build is out of scope).
	err = pullImage(sigCtx, dc, c.Service.Image)
	if err != nil {
		return err
	}

	mounts, err := serviceMounts(c.Service, c.service.workingDir)
	if err != nil {
		return err
	}

	containerName, err := containerNameFor(c.Service.Name)
	if err != nil {
		return err
	}

	cfg, hostCfg := c.containerConfigs(mounts)

	createResp, err := dc.ContainerCreate(sigCtx, cfg, hostCfg, nil, nil, containerName)
	if err != nil {
		return err
	}
	c.storeContainerID(createResp.ID)

	attachResp, err := dc.ContainerAttach(sigCtx, createResp.ID, container.AttachOptions{
		Stream: true,
		Stdin:  c.Stdin != nil,
		Stdout: true,
		Stderr: true,
		Logs:   true,
	})
	if err != nil {
		_ = forceRemoveContainer(context.Background(), dc, createResp.ID)
		return err
	}
	c.storeAttachState(&attachResp)

	stdout, stderr := c.normalizedWriters()
	c.startForwarding(attachResp, stdout, stderr)

	err = dc.ContainerStart(sigCtx, createResp.ID, container.StartOptions{})
	if err != nil {
		closeAttach(&attachResp)
		_ = forceRemoveContainer(context.Background(), dc, createResp.ID)
		return err
	}

	c.storeWait(dc, createResp.ID)
	return nil
}

type waitState struct {
	id          string
	dc          dockerAPI
	respCh      <-chan container.WaitResponse
	errCh       <-chan error
	attach      *dockertypes.HijackedResponse
	ioDone      chan struct{}
	stdinDone   chan struct{}
	sigCtx      context.Context
	stopSignals func()
}

func (c *Cmd) snapshotWaitState() (*waitState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return nil, errors.New("compose: not started")
	}
	if c.containerID == "" || c.docker == nil || c.waitRespCh == nil {
		return nil, errors.New("compose: internal state incomplete")
	}
	return &waitState{
		id:          c.containerID,
		dc:          c.docker,
		respCh:      c.waitRespCh,
		errCh:       c.waitErrCh,
		attach:      c.attach,
		ioDone:      c.ioDone,
		stdinDone:   c.stdinDone,
		sigCtx:      c.signalCtx,
		stopSignals: c.signalStop,
	}, nil
}

func waitForExit(
	ctx context.Context,
	sigCtx context.Context,
	dc dockerAPI,
	id string,
	respCh <-chan container.WaitResponse,
	errCh <-chan error,
) (container.WaitResponse, error) {
	stopOnce := sync.Once{}
	stopContainer := func() {
		stopOnce.Do(func() {
			_ = stopAndKill(context.Background(), dc, id, 2*time.Second)
		})
	}

	var waitResp container.WaitResponse
	for {
		select {
		case <-ctx.Done():
			stopContainer()
		case <-sigCtx.Done():
			stopContainer()
		case waitResp = <-respCh:
			return waitResp, nil
		case err := <-errCh:
			if err != nil {
				_ = forceRemoveContainer(context.Background(), dc, id)
				return container.WaitResponse{}, err
			}
		}
	}
}

func closeAttach(attach *dockertypes.HijackedResponse) {
	if attach == nil {
		return
	}
	_ = attach.CloseWrite()
	attach.Close()
}

func waitForIO(
	ctx context.Context,
	dc dockerAPI,
	id string,
	attach *dockertypes.HijackedResponse,
	stdinDone chan struct{},
	ioDone chan struct{},
) error {
	if stdinDone != nil {
		select {
		case <-stdinDone:
		case <-time.After(1 * time.Second):
		}
	}
	if ioDone != nil {
		select {
		case <-ioDone:
			return nil
		case <-ctx.Done():
			closeAttach(attach)
			_ = forceRemoveContainer(context.Background(), dc, id)
			return ctx.Err()
		}
	}
	return nil
}

// Wait waits for the started container to exit and returns its exit status.
func (c *Cmd) Wait(ctx context.Context) error {
	if ctx == nil {
		return errors.New("compose: ctx is required")
	}
	st, err := c.snapshotWaitState()
	if err != nil {
		return err
	}
	if st.stopSignals != nil {
		defer st.stopSignals()
	}

	waitResp, err := waitForExit(ctx, st.sigCtx, st.dc, st.id, st.respCh, st.errCh)
	if err != nil {
		return err
	}

	ioErr := waitForIO(ctx, st.dc, st.id, st.attach, st.stdinDone, st.ioDone)

	closeAttach(st.attach)

	if ioErr != nil {
		return ioErr
	}

	_ = forceRemoveContainer(context.Background(), st.dc, st.id)

	if waitResp.Error != nil {
		return errors.New(waitResp.Error.Message)
	}
	code := int(waitResp.StatusCode)
	if code != 0 {
		return &ExitError{Code: code, Stderr: c.stderrBuf.Bytes()}
	}
	return nil
}

// Output runs the command and returns its standard output.
func (c *Cmd) Output(ctx context.Context) ([]byte, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr

	err := c.Run(ctx)
	if err != nil {
		// Prefer stderr captured during run.
		if ee := (*ExitError)(nil); errors.As(err, &ee) {
			ee.Stderr = stderr.Bytes()
		}
		return stdout.Bytes(), err
	}
	return stdout.Bytes(), nil
}

// CombinedOutput runs the command and returns its combined standard output and standard error.
func (c *Cmd) CombinedOutput(ctx context.Context) ([]byte, error) {
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf

	err := c.Run(ctx)
	return buf.Bytes(), err
}

func pullImage(ctx context.Context, dc dockerAPI, ref string) error {
	if _, _, err := dc.ImageInspectWithRaw(ctx, ref); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return err
	}

	rc, err := dc.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return err
	}
	defer func() {
		_ = rc.Close()
	}()
	_, _ = io.Copy(io.Discard, rc)
	return nil
}

func containerNameFor(serviceName string) (string, error) {
	sfx, err := randSuffix(6)
	if err != nil {
		return "", err
	}
	base := "compose-exec"
	if serviceName != "" {
		base += "-" + sanitizeName(serviceName)
	}
	return base + "-" + sfx, nil
}

func sanitizeName(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_' || r == '.':
			return r
		default:
			return '-'
		}
	}, s)
	return strings.Trim(s, "-")
}

func serviceEnvSlice(svc types.ServiceConfig) []string {
	// compose-go resolves env_file/environment into svc.Environment.
	// MappingWithEquals preserves keys with empty values.
	if len(svc.Environment) == 0 {
		return nil
	}
	// types.MappingWithEquals supports ToSlice() in compose-go v2.
	if toSlice, ok := any(svc.Environment).(interface{ ToSlice() []string }); ok {
		return toSlice.ToSlice()
	}
	out := make([]string, 0, len(svc.Environment))
	for k, v := range svc.Environment {
		if v == nil {
			out = append(out, k)
			continue
		}
		out = append(out, k+"="+*v)
	}
	return out
}

func serviceMounts(svc types.ServiceConfig, baseDir string) ([]mount.Mount, error) {
	if len(svc.Volumes) == 0 {
		return nil, nil
	}

	baseDirAbs := baseDir
	if baseDirAbs != "" {
		baseDirAbs, _ = filepath.Abs(baseDirAbs)
	}

	out := make([]mount.Mount, 0, len(svc.Volumes))
	for _, v := range svc.Volumes {
		// Focus on bind mounts per SOW.
		typeStr := string(v.Type)
		if typeStr != "" && typeStr != string(types.VolumeTypeBind) {
			return nil, fmt.Errorf(
				"compose: unsupported volume type %q (only bind is supported)",
				typeStr,
			)
		}
		if strings.TrimSpace(v.Source) == "" {
			return nil, errors.New("compose: bind mount source is required")
		}
		src := v.Source
		if !filepath.IsAbs(src) {
			src = filepath.Join(baseDirAbs, src)
		}
		src, _ = filepath.Abs(src)

		m := mount.Mount{
			Type:     mount.TypeBind,
			Source:   src,
			Target:   v.Target,
			ReadOnly: v.ReadOnly,
		}
		out = append(out, m)
	}
	return out, nil
}

func stopAndKill(ctx context.Context, dc dockerAPI, id string, timeout time.Duration) error {
	seconds := int(timeout.Seconds())
	stopCtx, cancel := context.WithTimeout(ctx, timeout+1*time.Second)
	defer cancel()

	if err := dc.ContainerStop(stopCtx, id, container.StopOptions{Timeout: &seconds}); err != nil {
		killCtx, cancel2 := context.WithTimeout(ctx, 2*time.Second)
		defer cancel2()
		_ = dc.ContainerKill(killCtx, id, "SIGKILL")
	}

	return nil
}

func forceRemoveContainer(ctx context.Context, dc dockerAPI, id string) error {
	rmCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return dc.ContainerRemove(rmCtx, id, container.RemoveOptions{Force: true})
}

func ptr[T any](v T) *T { return &v }
