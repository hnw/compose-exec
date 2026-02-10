package compose

import (
	"context"
	"fmt"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
)

// resolveNetworking determines which network(s) to attach to.
// It iterates through all networks defined in the service config.
func (c *Cmd) resolveNetworking(_ context.Context, _ dockerAPI) *network.NetworkingConfig {
	if c.Service.NetworkMode != "" {
		return nil
	}

	if c.projectName() == "" {
		return nil
	}

	endpoints := make(map[string]*network.EndpointSettings)

	if len(c.Service.Networks) > 0 {
		for name := range c.Service.Networks {
			netName := fmt.Sprintf("%s_%s", c.projectName(), name)

			endpoints[netName] = &network.EndpointSettings{
				Aliases: []string{c.Service.Name},
			}
		}
	} else {
		netName := fmt.Sprintf("%s_default", c.projectName())
		endpoints[netName] = &network.EndpointSettings{
			Aliases: []string{c.Service.Name},
		}
	}

	return &network.NetworkingConfig{
		EndpointsConfig: endpoints,
	}
}

func (c *Cmd) ensureNetworks(
	ctx context.Context,
	dc dockerAPI,
	nc *network.NetworkingConfig,
) error {
	for netName := range nc.EndpointsConfig {
		list, err := dc.NetworkList(ctx, network.ListOptions{
			Filters: filters.NewArgs(filters.Arg("name", netName)),
		})
		if err != nil {
			return err
		}

		exists := false
		for _, n := range list {
			if n.Name == netName {
				exists = true
				break
			}
		}

		if exists {
			continue
		}

		_, err = dc.NetworkCreate(ctx, netName, network.CreateOptions{
			Labels: map[string]string{
				"com.docker.compose.project": c.projectName(),
				"com.docker.compose.network": "default",
			},
		})
		if err != nil {
			// If another process already created the network, ignore and continue.
			if isAlreadyExistsErr(err) {
				continue
			}
			return fmt.Errorf("failed to create network %q: %w", netName, err)
		}
	}
	return nil
}

func resolveVolumeName(projectName, volumeName string) string {
	projectName = strings.TrimSpace(projectName)
	volumeName = strings.TrimSpace(volumeName)
	if projectName == "" {
		return volumeName
	}
	return fmt.Sprintf("%s_%s", projectName, volumeName)
}

func (c *Cmd) ensureVolumes(ctx context.Context, dc dockerAPI) error {
	projectName := ""
	if c.service != nil {
		projectName = c.projectName()
	}

	if c.service != nil && c.service.project != nil && len(c.service.project.Volumes) > 0 {
		return ensureProjectVolumes(ctx, dc, projectName, c.service.project.Volumes)
	}
	return ensureServiceVolumes(ctx, dc, projectName, c.Service.Volumes)
}

func ensureProjectVolumes(
	ctx context.Context,
	dc dockerAPI,
	projectName string,
	volumesMap types.Volumes,
) error {
	for volName := range volumesMap {
		resolved := resolveVolumeName(projectName, volName)
		labels := map[string]string{
			"com.docker.compose.project": projectName,
			"com.docker.compose.volume":  volName,
		}
		if err := createVolumeIdempotent(ctx, dc, resolved, labels); err != nil {
			return err
		}
	}
	return nil
}

func ensureServiceVolumes(
	ctx context.Context,
	dc dockerAPI,
	projectName string,
	serviceVolumes []types.ServiceVolumeConfig,
) error {
	seen := map[string]struct{}{}
	for _, v := range serviceVolumes {
		if v.Type != types.VolumeTypeVolume {
			continue
		}
		name := strings.TrimSpace(v.Source)
		if name == "" {
			continue
		}
		resolved := resolveVolumeName(projectName, name)
		if _, ok := seen[resolved]; ok {
			continue
		}
		seen[resolved] = struct{}{}
		if err := createVolumeIdempotent(ctx, dc, resolved, nil); err != nil {
			return err
		}
	}
	return nil
}

func createVolumeIdempotent(
	ctx context.Context,
	dc dockerAPI,
	resolvedName string,
	labels map[string]string,
) error {
	_, err := dc.VolumeCreate(ctx, volume.CreateOptions{Name: resolvedName, Labels: labels})
	if err != nil {
		if isAlreadyExistsErr(err) {
			return nil
		}
		return fmt.Errorf("failed to create volume %q: %w", resolvedName, err)
	}
	return nil
}

func containerNameFor(serviceName string) (string, error) {
	sfx, err := randSuffix(6)
	if err != nil {
		return "", err
	}
	base := "compose-exec"
	if serviceName != "" {
		base += "-" + sanitizeName(serviceName)
	}
	return base + "-" + sfx, nil
}

func sanitizeName(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_' || r == '.':
			return r
		default:
			return '-'
		}
	}, s)
	return strings.Trim(s, "-")
}
