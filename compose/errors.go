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
