package compose

import (
	"fmt"
)

// ExitError is returned when a container exits with a non-zero status.
// It is analogous to os/exec.ExitError.
type ExitError struct {
	Code   int
	Stderr []byte
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("compose: exit status %d", e.Code)
}

// ExitCode returns the process exit status code.
func (e *ExitError) ExitCode() int { return e.Code }
