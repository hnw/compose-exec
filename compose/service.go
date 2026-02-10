package compose

import (
	"context"
	"os"
	"path/filepath"

	"github.com/compose-spec/compose-go/v2/types"
)

// Service is an execution context bound to a Compose service definition.
//
// It is intentionally small; lifecycle is managed per Cmd.
type Service struct {
	config     types.ServiceConfig
	workingDir string
	project    *Project
	loadErr    error
}

// newService creates a Service from a resolved service config.
//
// Relative paths (e.g. bind mount sources) are resolved relative to the project
// working directory or current working directory.
func newService(project *Project, config types.ServiceConfig) *Service {
	if project == nil {
		project = defaultProject()
	}
	wd := ""
	if project.WorkingDir != "" {
		wd = project.WorkingDir
	} else {
		cwd, _ := os.Getwd()
		wd, _ = filepath.Abs(cwd)
	}
	return &Service{
		config:     config,
		project:    project,
		workingDir: wd,
	}
}

// Command returns a Cmd to execute the given command arguments in the service.
//
// When called with zero args, Docker Engine/image defaults (or YAML service.command
// via command resolution) will be used. Use CommandContext to bind a context.
func (s *Service) Command(arg ...string) *Cmd {
	args := append([]string(nil), arg...)
	return &Cmd{
		Service: s.config,
		Args:    args,
		loadErr: s.loadErr,
		service: s,
	}
}

// CommandContext returns a Cmd to execute the given command arguments in the service,
// bound to the provided context for lifecycle cancellation.
//
// It panics if ctx is nil, matching os/exec.CommandContext behavior.
func (s *Service) CommandContext(ctx context.Context, arg ...string) *Cmd {
	if ctx == nil {
		panic("nil Context")
	}
	args := append([]string(nil), arg...)
	return &Cmd{
		Service: s.config,
		Args:    args,
		loadErr: s.loadErr,
		service: s,
		ctx:     ctx,
	}
}
