package compose

import (
	"fmt"

	"github.com/docker/docker/api/types/container"
)

// ExitError is returned when a container exits with a non-zero status.
// It is analogous to os/exec.ExitError (ContainerState mirrors ProcessState).
type ExitError struct {
	// Code is the exit status from the wait response.
	Code int
	// Stderr is a snapshot of standard error when captured by Output.
	Stderr []byte
	// ContainerState is the last known container state from Docker inspect.
	// It is nil if inspect fails.
	ContainerState *container.State
}

func (e *ExitError) Error() string {
	base := fmt.Sprintf("compose: exit status %d", e.Code)
	if len(e.Stderr) == 0 {
		return base
	}

	const maxSnippetLen = 512
	snippet := e.Stderr

	prefix := ""
	if len(snippet) > maxSnippetLen {
		snippet = snippet[len(snippet)-maxSnippetLen:]
		prefix = "... "
	}

	return fmt.Sprintf("%s: stderr=%s%q", base, prefix, string(snippet))
}

// ExitCode returns the process exit status code.
func (e *ExitError) ExitCode() int { return e.Code }

// Pid returns the container's process ID, or 0 if unavailable.
func (e *ExitError) Pid() int {
	if e.ContainerState != nil {
		return e.ContainerState.Pid
	}
	return 0
}
