package compose

import (
	"context"
	"errors"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
)

func (c *Cmd) contextOrBackground() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
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

func (c *Cmd) isStarted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started
}

func (c *Cmd) ensureService() {
	if c.service == nil {
		// Allow constructing Cmd manually, but then working dir resolution uses process cwd.
		c.service = newService(defaultProject(), c.Service)
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

func ptr[T any](v T) *T { return &v }
