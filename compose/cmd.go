// Package compose provides a small API to execute commands in Compose services via Docker Engine.
package compose

import (
	"bytes"
	"context"
	"io"
	"sync"

	"github.com/compose-spec/compose-go/v2/types"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
)

// Cmd represents a pending command execution, similar to os/exec.Cmd.
type Cmd struct {
	// Public fields
	Service types.ServiceConfig
	Args    []string
	Env     []string
	// WorkingDir overrides the docker-compose.yml working_dir for this Cmd.
	// Leave empty to use the service config or image default.
	WorkingDir string

	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// Delayed error propagated from Service initialization.
	loadErr error
	// ctx is the lifecycle context (set by CommandContext).
	ctx context.Context

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

	stdoutPipe *io.PipeWriter
	stderrPipe *io.PipeWriter
	stdinPipe  *io.PipeReader
}
