package compose

import (
	"context"
	"errors"
	"fmt"

	"github.com/compose-spec/compose-go/v2/types"
)

// Project is a compose-go project with helper methods for compose-exec.
type Project types.Project

func defaultProject() *Project {
	return &Project{Name: "default"}
}

// Service returns a Service bound to the named compose service.
func (p *Project) Service(name string) (*Service, error) {
	if p == nil {
		return nil, errors.New("compose: project is nil")
	}
	cfg, err := findService(p.Services, name)
	if err != nil {
		return nil, err
	}
	return newService(p, cfg), nil
}

// Command returns a Cmd to execute args in the named service.
func (p *Project) Command(service string, arg ...string) *Cmd {
	svc, err := p.Service(service)
	if err != nil {
		return &Cmd{
			Args:    append([]string(nil), arg...),
			loadErr: err,
		}
	}
	return svc.Command(arg...)
}

// CommandContext returns a Cmd bound to ctx to execute args in the named service.
func (p *Project) CommandContext(ctx context.Context, service string, arg ...string) *Cmd {
	if ctx == nil {
		panic("nil Context")
	}
	svc, err := p.Service(service)
	if err != nil {
		return &Cmd{
			Args:    append([]string(nil), arg...),
			loadErr: err,
			ctx:     ctx,
		}
	}
	return svc.CommandContext(ctx, arg...)
}

func findService(services types.Services, name string) (types.ServiceConfig, error) {
	for _, s := range services {
		if s.Name == name {
			return s, nil
		}
	}
	return types.ServiceConfig{}, fmt.Errorf("compose: service %q not found", name)
}
