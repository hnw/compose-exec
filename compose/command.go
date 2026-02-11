package compose

import (
	"context"
	"os"
)

// Command returns a Cmd to execute the given args in the named service.
// It loads the compose project from the current working directory.
//
// Note: Each call loads project configuration. For repeated invocations,
// use LoadProject once and reuse Project.Command().
func Command(service string, arg ...string) *Cmd {
	return commandWithContext(context.Background(), service, arg...)
}

// CommandContext returns a Cmd to execute the given args in the named service,
// bound to the provided context for lifecycle cancellation.
//
// Note: Each call loads project configuration. For repeated invocations,
// use LoadProject once and reuse Project.CommandContext().
func CommandContext(ctx context.Context, service string, arg ...string) *Cmd {
	if ctx == nil {
		panic("nil Context")
	}
	return commandWithContext(ctx, service, arg...)
}

func commandWithContext(ctx context.Context, service string, arg ...string) *Cmd {
	wd, err := os.Getwd()
	if err != nil {
		return cmdWithLoadErr(ctx, err, arg)
	}

	// Best-effort diagnostic: help users running this library inside a container
	// where the host project directory wasn't mirror-mounted.
	maybeWarnMissingComposeFileInContainer(wd)

	proj, err := LoadProject(context.Background(), wd)
	if err != nil {
		return cmdWithLoadErr(ctx, err, arg)
	}
	svc, err := proj.Service(service)
	if err != nil {
		return cmdWithLoadErr(ctx, err, arg)
	}
	if ctx != context.Background() {
		return svc.CommandContext(ctx, arg...)
	}
	return svc.Command(arg...)
}

func cmdWithLoadErr(ctx context.Context, err error, arg []string) *Cmd {
	return &Cmd{
		Args:    append([]string(nil), arg...),
		loadErr: err,
		ctx:     ctx,
	}
}
