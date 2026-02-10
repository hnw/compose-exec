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
	project    *types.Project
	loadErr    error
}

// NewService creates a Service from a resolved service config.
//
// Relative paths (e.g. bind mount sources) are resolved relative to the current
// working directory.
func NewService(project *types.Project, config types.ServiceConfig) *Service {
	if project == nil {
		project = &types.Project{Name: "default"}
	}
	cwd, _ := os.Getwd()
	cwd, _ = filepath.Abs(cwd)
	return &Service{
		config:     config,
		project:    project,
		workingDir: cwd,
	}
}

// From loads the compose project in the current directory and returns a Service
// bound to the named service.
//
// Errors are stored in the returned Service and will be returned later when
// Command().Run()/Start() is called (delayed error pattern).
func From(serviceName string) *Service {
	wd, err := os.Getwd()
	if err != nil {
		return &Service{loadErr: err}
	}

	// Best-effort diagnostic: help users running this library inside a container
	// where the host project directory wasn't mirror-mounted.
	maybeWarnMissingComposeFileInContainer(wd)

	ctx := context.Background()
	proj, err := LoadProject(ctx, wd)
	if err != nil {
		return &Service{loadErr: err}
	}

	svcConfig, err := findService(proj, serviceName)
	if err != nil {
		return &Service{loadErr: err}
	}

	s := NewService(proj, svcConfig)
	if proj.WorkingDir != "" {
		s.workingDir = proj.WorkingDir
	}
	return s
}

// FromProject creates a Service from a project and service name.
//
// This helper is not required by the SOW public API, but simplifies correct
// resolution of relative paths.
func FromProject(project *types.Project, serviceName string) (*Service, error) {
	cfg, err := findService(project, serviceName)
	if err != nil {
		return nil, err
	}
	wd := project.WorkingDir
	if wd == "" {
		wd, _ = os.Getwd()
		wd, _ = filepath.Abs(wd)
	}
	s := NewService(project, cfg)
	s.workingDir = wd
	return s, nil
}

// Command returns a Cmd to execute the given command arguments in the service.
//
// When called with zero args, Docker Engine/image defaults (or YAML service.command
// via command resolution) will be used.
func (s *Service) Command(arg ...string) *Cmd {
	args := append([]string(nil), arg...)
	return &Cmd{
		Service: s.config,
		Args:    args,
		loadErr: s.loadErr,
		service: s,
	}
}
