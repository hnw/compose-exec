package compose

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/docker/docker/api/types/container"
)

// Run starts the container and waits for it to exit, similar to (*exec.Cmd).Run.
// If created via CommandContext, its context controls cancellation.
func (c *Cmd) Run() error {
	if c.loadErr != nil {
		return c.loadErr
	}
	if err := c.Start(); err != nil {
		return err
	}
	return c.Wait()
}

// Environ returns a copy of the environment in which the command would run.
func (c *Cmd) Environ() []string {
	env := mergeEnv(serviceEnvSlice(c.Service), c.Env)
	return append([]string(nil), env...)
}

// Start creates and starts the container for the configured service command.
//
//nolint:gocyclo // Orchestrates container lifecycle with explicit error handling.
func (c *Cmd) Start() (startErr error) {
	if c.loadErr != nil {
		return c.loadErr
	}
	if err := c.markStarted(); err != nil {
		return err
	}
	defer func() {
		if startErr != nil {
			c.closePipes(startErr)
		}
	}()
	ctx := c.contextOrBackground()
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
		Logs:   false,
	})
	if err != nil {
		_ = forceRemoveContainer(context.Background(), dc, createResp.ID)
		return err
	}
	c.storeAttachState(&attachResp)

	stdout, stderr := c.normalizedWriters()
	// Ensure stdout/stderr forwarder is running before starting the container.
	ioReady := c.startForwarding(attachResp, stdout, stderr)
	<-ioReady

	err = dc.ContainerStart(sigCtx, createResp.ID, container.StartOptions{})
	if err != nil {
		closeAttach(&attachResp)
		_ = forceRemoveContainer(context.Background(), dc, createResp.ID)
		return err
	}

	c.storeWait(dc, createResp.ID)
	return nil
}

// Output runs the command and returns its standard output.
// If created via CommandContext, its context controls cancellation.
func (c *Cmd) Output() ([]byte, error) {
	if c.Stdout != nil {
		return nil, errors.New("compose: Stdout already set")
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	c.Stdout = &stdout
	capture := false
	if c.Stderr == nil {
		c.Stderr = &stderr
		c.captureStderr = true
		capture = true
		defer func() { c.captureStderr = false }()
	}

	err := c.Run()
	if err != nil {
		// Prefer stderr captured during run.
		if capture {
			if ee := (*ExitError)(nil); errors.As(err, &ee) {
				ee.Stderr = stderr.Bytes()
			}
		}
		return stdout.Bytes(), err
	}
	return stdout.Bytes(), nil
}

// CombinedOutput runs the command and returns its combined standard output and standard error.
// If created via CommandContext, its context controls cancellation.
func (c *Cmd) CombinedOutput() ([]byte, error) {
	if c.Stdout != nil || c.Stderr != nil {
		return nil, errors.New("compose: Stdout or Stderr already set")
	}
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf

	err := c.Run()
	return buf.Bytes(), err
}
