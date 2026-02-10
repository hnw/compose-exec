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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/compose-spec/compose-go/v2/types"
	cerrdefs "github.com/containerd/errdefs"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
)

// Cmd represents a pending command execution, similar to os/exec.Cmd.
type Cmd struct {
	// Public fields
	Service types.ServiceConfig
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
	// dockerOwned is true when this Cmd created the client internally.
	dockerOwned bool

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

	captureStderr bool
	stderrBuf     bytes.Buffer
}

// String returns a human-friendly representation of the command.
//
// When Args is empty, it returns "<default>" to indicate that Docker Engine/image
// defaults (or YAML service.command via resolution) will be used.
func (c *Cmd) String() string {
	if len(c.Args) == 0 {
		return "<default>"
	}
	parts := make([]string, 0, len(c.Args))
	for _, a := range c.Args {
		if needsQuoting(a) {
			parts = append(parts, strconv.Quote(a))
			continue
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

func needsQuoting(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if unicode.IsSpace(r) || r == '"' || r == '\\' {
			return true
		}
	}
	return false
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
		c.service = NewService(&types.Project{Name: "default"}, c.Service)
	}
}

func (c *Cmd) projectName() string {
	if c.service == nil || c.service.project == nil {
		return ""
	}
	return c.service.project.Name
}

func (c *Cmd) resolveCommand() {
	// Command resolution priority:
	// 1) Explicit args
	// 2) Service.Command from YAML
	// 3) Delegate to image defaults (no error)
	if len(c.Args) == 0 && len(c.Service.Command) > 0 {
		c.Args = []string(c.Service.Command)
	}
}

func (c *Cmd) storeSignal(sigCtx context.Context, stopSignals func()) {
	c.mu.Lock()
	c.signalCtx = sigCtx
	c.signalStop = stopSignals
	c.mu.Unlock()
}

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

func (c *Cmd) normalizedWriters() (io.Writer, io.Writer) {
	stdout := c.Stdout
	stderr := c.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if c.captureStderr {
		// Reset per run; only capture when explicitly enabled (Output/CombinedOutput).
		c.stderrBuf.Reset()
		stderr = io.MultiWriter(stderr, &c.stderrBuf)
	} else {
		// Avoid returning stale stderr from previous runs.
		c.stderrBuf.Reset()
	}
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

	exposedPorts := nat.PortSet{}
	portBindings := nat.PortMap{}

	for _, p := range c.Service.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}

		portKey := nat.Port(fmt.Sprintf("%d/%s", p.Target, proto))

		exposedPorts[portKey] = struct{}{}

		if p.Published != "" {
			binding := nat.PortBinding{
				HostIP:   p.HostIP,
				HostPort: p.Published,
			}
			portBindings[portKey] = append(portBindings[portKey], binding)
		}
	}

	labels := map[string]string{}
	for k, v := range c.Service.Labels {
		labels[k] = v
	}
	if proj := c.projectName(); proj != "" {
		labels["com.docker.compose.project"] = proj
	}
	if svc := strings.TrimSpace(c.Service.Name); svc != "" {
		labels["com.docker.compose.service"] = svc
	}
	if len(labels) == 0 {
		labels = nil
	}

	cfg := &container.Config{
		Image:      c.Service.Image,
		WorkingDir: c.Service.WorkingDir,
		Env:        env,
		Labels:     labels,
		// TODO: Future support for TTY (out of scope).
		Tty:          false,
		OpenStdin:    c.Stdin != nil,
		StdinOnce:    false,
		ExposedPorts: exposedPorts,
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
		Init:         ptr(initEnabled),
		Mounts:       mounts,
		PortBindings: portBindings,
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
//
//nolint:gocyclo // Orchestrates container lifecycle with explicit error handling.
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
	if c.Service.Build != nil {
		return errors.New("compose: service.build is not supported (use a pre-built image)")
	}
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
	defer func() {
		if startErr != nil {
			c.closeDockerIfOwned()
		}
	}()

	// Pull image (build is out of scope).
	err = pullImage(sigCtx, dc, c.Service.Image)
	if err != nil {
		return err
	}

	mounts, err := serviceMounts(c.Service, c.service.workingDir, c.projectName())
	if err != nil {
		return err
	}

	containerName, err := containerNameFor(c.Service.Name)
	if err != nil {
		return err
	}

	cfg, hostCfg := c.containerConfigs(mounts)

	networkingCfg := c.resolveNetworking(sigCtx, dc)

	if networkingCfg != nil {
		if netErr := c.ensureNetworks(sigCtx, dc, networkingCfg); netErr != nil {
			return netErr
		}
	}

	if volErr := c.ensureVolumes(sigCtx, dc); volErr != nil {
		return volErr
	}

	createResp, err := dc.ContainerCreate(sigCtx, cfg, hostCfg, networkingCfg, nil, containerName)
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

func (c *Cmd) ensureNetworks(
	ctx context.Context,
	dc dockerAPI,
	nc *network.NetworkingConfig,
) error {
	for netName := range nc.EndpointsConfig {
		list, err := dc.NetworkList(ctx, network.ListOptions{
			Filters: filters.NewArgs(filters.Arg("name", netName)),
		})
		if err != nil {
			return err
		}

		exists := false
		for _, n := range list {
			if n.Name == netName {
				exists = true
				break
			}
		}

		if exists {
			continue
		}

		_, err = dc.NetworkCreate(ctx, netName, network.CreateOptions{
			Labels: map[string]string{
				"com.docker.compose.project": c.projectName(),
				"com.docker.compose.network": "default",
			},
		})
		if err != nil {
			// If another process already created the network, ignore and continue.
			if cerrdefs.IsAlreadyExists(err) || strings.Contains(err.Error(), "already exists") {
				continue
			}
			return fmt.Errorf("failed to create network %q: %w", netName, err)
		}
	}
	return nil
}

func resolveVolumeName(projectName, volumeName string) string {
	projectName = strings.TrimSpace(projectName)
	volumeName = strings.TrimSpace(volumeName)
	if projectName == "" {
		return volumeName
	}
	return fmt.Sprintf("%s_%s", projectName, volumeName)
}

func (c *Cmd) ensureVolumes(ctx context.Context, dc dockerAPI) error {
	projectName := ""
	if c.service != nil {
		projectName = c.projectName()
	}

	if c.service != nil && c.service.project != nil && len(c.service.project.Volumes) > 0 {
		return ensureProjectVolumes(ctx, dc, projectName, c.service.project.Volumes)
	}
	return ensureServiceVolumes(ctx, dc, projectName, c.Service.Volumes)
}

func ensureProjectVolumes(
	ctx context.Context,
	dc dockerAPI,
	projectName string,
	volumesMap types.Volumes,
) error {
	for volName := range volumesMap {
		resolved := resolveVolumeName(projectName, volName)
		labels := map[string]string{
			"com.docker.compose.project": projectName,
			"com.docker.compose.volume":  volName,
		}
		if err := createVolumeIdempotent(ctx, dc, resolved, labels); err != nil {
			return err
		}
	}
	return nil
}

func ensureServiceVolumes(
	ctx context.Context,
	dc dockerAPI,
	projectName string,
	serviceVolumes []types.ServiceVolumeConfig,
) error {
	seen := map[string]struct{}{}
	for _, v := range serviceVolumes {
		if v.Type != types.VolumeTypeVolume {
			continue
		}
		name := strings.TrimSpace(v.Source)
		if name == "" {
			continue
		}
		resolved := resolveVolumeName(projectName, name)
		if _, ok := seen[resolved]; ok {
			continue
		}
		seen[resolved] = struct{}{}
		if err := createVolumeIdempotent(ctx, dc, resolved, nil); err != nil {
			return err
		}
	}
	return nil
}

func createVolumeIdempotent(
	ctx context.Context,
	dc dockerAPI,
	resolvedName string,
	labels map[string]string,
) error {
	_, err := dc.VolumeCreate(ctx, volume.CreateOptions{Name: resolvedName, Labels: labels})
	if err != nil {
		if cerrdefs.IsAlreadyExists(err) || strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return fmt.Errorf("failed to create volume %q: %w", resolvedName, err)
	}
	return nil
}

// resolveNetworking determines which network(s) to attach to.
// It iterates through all networks defined in the service config.
func (c *Cmd) resolveNetworking(_ context.Context, _ dockerAPI) *network.NetworkingConfig {
	if c.Service.NetworkMode != "" {
		return nil
	}

	if c.projectName() == "" {
		return nil
	}

	endpoints := make(map[string]*network.EndpointSettings)

	if len(c.Service.Networks) > 0 {
		for name := range c.Service.Networks {
			netName := fmt.Sprintf("%s_%s", c.projectName(), name)

			endpoints[netName] = &network.EndpointSettings{
				Aliases: []string{c.Service.Name},
			}
		}
	} else {
		netName := fmt.Sprintf("%s_default", c.projectName())
		endpoints[netName] = &network.EndpointSettings{
			Aliases: []string{c.Service.Name},
		}
	}

	return &network.NetworkingConfig{
		EndpointsConfig: endpoints,
	}
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
		case err, ok := <-errCh:
			if !ok {
				// errCh closed; disable this select case to avoid spinning.
				errCh = nil
				continue
			}
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
	defer c.closeDockerIfOwned()
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
	c.captureStderr = true
	defer func() { c.captureStderr = false }()

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
	c.captureStderr = true
	defer func() { c.captureStderr = false }()

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

func serviceMounts(
	svc types.ServiceConfig,
	baseDir string,
	projectName string,
) ([]mount.Mount, error) {
	if len(svc.Volumes) == 0 {
		return nil, nil
	}

	baseDirAbs := baseDir
	if baseDirAbs != "" {
		baseDirAbs, _ = filepath.Abs(baseDirAbs)
	}

	out := make([]mount.Mount, 0, len(svc.Volumes))
	for _, v := range svc.Volumes {
		typeStr := string(v.Type)
		switch {
		case typeStr == "" || v.Type == types.VolumeTypeBind:
			if strings.TrimSpace(v.Source) == "" {
				return nil, errors.New("compose: bind mount source is required")
			}
			src := v.Source
			if !filepath.IsAbs(src) {
				src = filepath.Join(baseDirAbs, src)
			}
			src, _ = filepath.Abs(src)

			out = append(out, mount.Mount{
				Type:     mount.TypeBind,
				Source:   src,
				Target:   v.Target,
				ReadOnly: v.ReadOnly,
			})

		case v.Type == types.VolumeTypeVolume:
			src := strings.TrimSpace(v.Source)
			if src != "" {
				src = resolveVolumeName(projectName, src)
			}
			out = append(out, mount.Mount{
				Type:     mount.TypeVolume,
				Source:   src,
				Target:   v.Target,
				ReadOnly: v.ReadOnly,
			})

		default:
			return nil, fmt.Errorf(
				"compose: unsupported volume type %q (supported: bind, volume)",
				typeStr,
			)
		}
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
