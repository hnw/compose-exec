package compose

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/go-connections/nat"
)

func (c *Cmd) containerConfigs(mounts []mount.Mount) (*container.Config, *container.HostConfig) {
	// Respect docker-compose.yml as the source of truth for user.
	// If empty, omit and let Docker Engine/image defaults apply.
	user := strings.TrimSpace(c.Service.User)

	initEnabled := true
	if c.Service.Init != nil {
		initEnabled = *c.Service.Init
	}

	envBase := serviceEnvSlice(c.Service)
	env := mergeEnv(envBase, c.Env)

	exposedPorts := nat.PortSet{}
	portBindings := nat.PortMap{}

	for _, p := range c.Service.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}

		portKey := nat.Port(fmt.Sprintf("%d/%s", p.Target, proto))

		exposedPorts[portKey] = struct{}{}

		if p.Published != "" {
			binding := nat.PortBinding{
				HostIP:   p.HostIP,
				HostPort: p.Published,
			}
			portBindings[portKey] = append(portBindings[portKey], binding)
		}
	}

	labels := map[string]string{}
	for k, v := range c.Service.Labels {
		labels[k] = v
	}
	if proj := c.projectName(); proj != "" {
		labels["com.docker.compose.project"] = proj
	}
	if svc := strings.TrimSpace(c.Service.Name); svc != "" {
		labels["com.docker.compose.service"] = svc
	}
	if len(labels) == 0 {
		labels = nil
	}

	workingDir := c.Service.WorkingDir
	if c.WorkingDir != "" {
		workingDir = c.WorkingDir
	}

	cfg := &container.Config{
		Image:      c.Service.Image,
		WorkingDir: workingDir,
		Env:        env,
		Labels:     labels,
		// TODO: Future support for TTY (out of scope).
		Tty:          false,
		OpenStdin:    stdinEnabled(c.Stdin),
		StdinOnce:    false,
		ExposedPorts: exposedPorts,
	}
	if hc := c.Service.HealthCheck; hc != nil {
		cfg.Healthcheck = dockerHealthConfig(hc)
	}
	if user != "" {
		cfg.User = user
	}
	if len(c.Args) > 0 {
		cfg.Cmd = c.Args
	}
	if len(c.Service.Entrypoint) > 0 {
		cfg.Entrypoint = []string(c.Service.Entrypoint)
	}

	hostCfg := &container.HostConfig{
		Init:         ptr(initEnabled),
		Mounts:       mounts,
		PortBindings: portBindings,
	}
	if c.Service.MemLimit > 0 {
		hostCfg.Memory = int64(c.Service.MemLimit)
	}
	if c.Service.MemReservation > 0 {
		hostCfg.MemoryReservation = int64(c.Service.MemReservation)
	}
	if c.Service.MemSwapLimit > 0 {
		hostCfg.MemorySwap = int64(c.Service.MemSwapLimit)
	}
	applyHostSecurityConfig(hostCfg, c.Service)
	if nm := strings.TrimSpace(c.Service.NetworkMode); nm != "" {
		hostCfg.NetworkMode = container.NetworkMode(nm)
	}
	return cfg, hostCfg
}

func dockerHealthConfig(hc *types.HealthCheckConfig) *container.HealthConfig {
	dockerHC := &container.HealthConfig{}
	if hc.Disable {
		dockerHC.Test = []string{"NONE"}
		return dockerHC
	}

	dockerHC.Test = []string(hc.Test)
	if hc.Interval != nil {
		dockerHC.Interval = time.Duration(*hc.Interval)
	}
	if hc.Timeout != nil {
		dockerHC.Timeout = time.Duration(*hc.Timeout)
	}
	if hc.StartPeriod != nil {
		dockerHC.StartPeriod = time.Duration(*hc.StartPeriod)
	}
	if hc.StartInterval != nil {
		dockerHC.StartInterval = time.Duration(*hc.StartInterval)
	}
	if hc.Retries != nil {
		retries := *hc.Retries
		if retries > uint64(math.MaxInt) {
			dockerHC.Retries = math.MaxInt
		} else {
			dockerHC.Retries = int(retries)
		}
	}
	return dockerHC
}

func applyHostSecurityConfig(hostCfg *container.HostConfig, svc types.ServiceConfig) {
	if hostCfg == nil {
		return
	}
	hostCfg.Privileged = svc.Privileged
	if len(svc.CapAdd) > 0 {
		hostCfg.CapAdd = append(hostCfg.CapAdd, svc.CapAdd...)
	}
	if len(svc.CapDrop) > 0 {
		hostCfg.CapDrop = append(hostCfg.CapDrop, svc.CapDrop...)
	}
}

func serviceEnvSlice(svc types.ServiceConfig) []string {
	// compose-go resolves env_file/environment into svc.Environment.
	// MappingWithEquals preserves keys with empty values.
	if len(svc.Environment) == 0 {
		return nil
	}
	// types.MappingWithEquals supports ToSlice() in compose-go v2.
	if toSlice, ok := any(svc.Environment).(interface{ ToSlice() []string }); ok {
		return toSlice.ToSlice()
	}
	out := make([]string, 0, len(svc.Environment))
	for k, v := range svc.Environment {
		if v == nil {
			out = append(out, k)
			continue
		}
		out = append(out, k+"="+*v)
	}
	return out
}

func serviceMounts(
	svc types.ServiceConfig,
	baseDir string,
	projectName string,
	projectVolumes types.Volumes,
) ([]mount.Mount, error) {
	if len(svc.Volumes) == 0 {
		return nil, nil
	}

	baseDirAbs := baseDir
	if baseDirAbs != "" {
		baseDirAbs, _ = filepath.Abs(baseDirAbs)
	}

	out := make([]mount.Mount, 0, len(svc.Volumes))
	for _, v := range svc.Volumes {
		typeStr := string(v.Type)
		switch {
		case typeStr == "" || v.Type == types.VolumeTypeBind:
			if strings.TrimSpace(v.Source) == "" {
				return nil, errors.New("compose: bind mount source is required")
			}
			src := v.Source
			if !filepath.IsAbs(src) {
				src = filepath.Join(baseDirAbs, src)
			}
			src, _ = filepath.Abs(src)

			out = append(out, mount.Mount{
				Type:     mount.TypeBind,
				Source:   src,
				Target:   v.Target,
				ReadOnly: v.ReadOnly,
			})

		case v.Type == types.VolumeTypeVolume:
			src := strings.TrimSpace(v.Source)
			if src != "" {
				src = resolveVolumeSource(projectName, src, projectVolumes)
			}
			out = append(out, mount.Mount{
				Type:     mount.TypeVolume,
				Source:   src,
				Target:   v.Target,
				ReadOnly: v.ReadOnly,
			})

		default:
			return nil, fmt.Errorf(
				"compose: unsupported volume type %q (supported: bind, volume)",
				typeStr,
			)
		}
	}
	return out, nil
}
