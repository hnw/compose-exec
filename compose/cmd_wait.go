package compose

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
)

// Wait waits for the started container to exit and returns its exit status.
// If created via CommandContext, its context controls cancellation.
func (c *Cmd) Wait() error {
	ctx := c.contextOrBackground()
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

	code := int(waitResp.StatusCode)
	var exitState *container.State
	if waitResp.Error == nil && code != 0 {
		exitState = captureContainerState(st.dc, st.id)
	}

	_ = forceRemoveContainer(context.Background(), st.dc, st.id)

	if waitResp.Error != nil {
		return errors.New(waitResp.Error.Message)
	}
	if code != 0 {
		return &ExitError{
			Code:           code,
			Stderr:         c.stderrBuf.Bytes(),
			ContainerState: exitState,
		}
	}
	return nil
}

// WaitUntilHealthy blocks until the started container becomes healthy.
// If created via CommandContext, its context controls cancellation.
//
// Strict behavior:
// - If the service has no healthcheck defined, it returns an error immediately.
// - If the container becomes unhealthy or stops running, it returns an error immediately.
func (c *Cmd) WaitUntilHealthy() error {
	if c.loadErr != nil {
		return c.loadErr
	}
	ctx := c.contextOrBackground()
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

func captureContainerState(dc dockerAPI, containerID string) *container.State {
	if dc == nil || containerID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	j, err := dc.ContainerInspect(ctx, containerID)
	if err != nil || j.State == nil {
		return nil
	}
	return j.State
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
