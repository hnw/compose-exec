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
	loadErr    error
}

// NewService creates a Service from a resolved service config.
//
// Relative paths (e.g. bind mount sources) are resolved relative to the current
// working directory.
func NewService(config types.ServiceConfig) *Service {
	cwd, _ := os.Getwd()
	cwd, _ = filepath.Abs(cwd)
	return &Service{config: config, workingDir: cwd}
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

	s := NewService(svcConfig)
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
	s := NewService(cfg)
	s.workingDir = wd
	return s, nil
}

// Command returns a Cmd to execute the given command name and arguments in the service.
func (s *Service) Command(name string, arg ...string) *Cmd {
	args := make([]string, 0, 1+len(arg))
	args = append(args, name)
	args = append(args, arg...)

	return &Cmd{
		Service: s.config,
		Path:    name,
		Args:    args,
		loadErr: s.loadErr,
		service: s,
	}
}
