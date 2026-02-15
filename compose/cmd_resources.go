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

type resolvedNetworking struct {
	config *network.NetworkingConfig
	specs  map[string]networkSpec
}

type networkSpec struct {
	key      string
	declared bool
	config   types.NetworkConfig
}

// resolveNetworking determines which network(s) to attach to.
// It iterates through all networks defined in the service config.
func (c *Cmd) resolveNetworking(_ context.Context, _ dockerAPI) *resolvedNetworking {
	if c.Service.NetworkMode != "" {
		return nil
	}

	endpoints := make(map[string]*network.EndpointSettings)
	specs := make(map[string]networkSpec)
	projectNetworks := c.projectNetworks()
	projectName := c.projectName()

	if len(c.Service.Networks) > 0 {
		for key, svcNetCfg := range c.Service.Networks {
			netName := resolveNetworkName(projectName, key, projectNetworks)
			if netName == "" {
				continue
			}
			endpoints[netName] = endpointSettings(c.Service.Name, svcNetCfg)
			specs[netName] = networkSpecFor(key, projectNetworks)
		}
	} else {
		netName := resolveNetworkName(projectName, "default", projectNetworks)
		if netName != "" {
			endpoints[netName] = endpointSettings(c.Service.Name, nil)
			specs[netName] = networkSpecFor("default", projectNetworks)
		}
	}

	if len(endpoints) == 0 {
		return nil
	}

	return &resolvedNetworking{
		config: &network.NetworkingConfig{
			EndpointsConfig: endpoints,
		},
		specs: specs,
	}
}

func (c *Cmd) ensureNetworks(
	ctx context.Context,
	dc dockerAPI,
	nc *resolvedNetworking,
) error {
	if nc == nil || nc.config == nil {
		return nil
	}

	for netName := range nc.config.EndpointsConfig {
		spec := nc.specs[netName]
		if spec.declared && bool(spec.config.External) {
			// External networks must already exist; never create them.
			continue
		}

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

		_, err = dc.NetworkCreate(ctx, netName, networkCreateOptions(c.projectName(), spec))
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

func networkSpecFor(key string, projectNetworks types.Networks) networkSpec {
	spec := networkSpec{key: key}
	if cfg, ok := projectNetworks[key]; ok {
		spec.declared = true
		spec.config = cfg
	}
	return spec
}

func networkCreateOptions(projectName string, spec networkSpec) network.CreateOptions {
	opts := network.CreateOptions{}
	labels := make(map[string]string)

	if spec.declared {
		cfg := spec.config
		if driver := strings.TrimSpace(cfg.Driver); driver != "" {
			opts.Driver = driver
		}
		if len(cfg.DriverOpts) > 0 {
			opts.Options = copyStringMap(cfg.DriverOpts)
		}
		opts.Internal = cfg.Internal
		opts.Attachable = cfg.Attachable
		opts.EnableIPv4 = cloneBoolPtr(cfg.EnableIPv4)
		opts.EnableIPv6 = cloneBoolPtr(cfg.EnableIPv6)
		opts.IPAM = dockerIPAMConfig(cfg.Ipam)

		for k, v := range cfg.Labels {
			labels[k] = v
		}
	}

	if projectName != "" {
		labels["com.docker.compose.project"] = projectName
	}
	if spec.key != "" {
		labels["com.docker.compose.network"] = spec.key
	}
	if len(labels) > 0 {
		opts.Labels = labels
	}

	return opts
}

func resolveNetworkName(projectName, networkKey string, projectNetworks types.Networks) string {
	networkKey = strings.TrimSpace(networkKey)
	if networkKey == "" {
		return ""
	}
	if cfg, ok := projectNetworks[networkKey]; ok {
		return resolveResourceName(projectName, networkKey, cfg.Name, bool(cfg.External))
	}
	return resolveVolumeName(projectName, networkKey)
}

func endpointSettings(
	serviceName string,
	cfg *types.ServiceNetworkConfig,
) *network.EndpointSettings {
	settings := &network.EndpointSettings{
		Aliases: endpointAliases(serviceName, cfg),
	}

	if cfg == nil {
		return settings
	}
	if len(cfg.DriverOpts) > 0 {
		settings.DriverOpts = copyStringMap(cfg.DriverOpts)
	}
	settings.GwPriority = cfg.GatewayPriority
	settings.MacAddress = cfg.MacAddress

	if cfg.Ipv4Address != "" || cfg.Ipv6Address != "" || len(cfg.LinkLocalIPs) > 0 {
		settings.IPAMConfig = &network.EndpointIPAMConfig{
			IPv4Address:  cfg.Ipv4Address,
			IPv6Address:  cfg.Ipv6Address,
			LinkLocalIPs: append([]string(nil), cfg.LinkLocalIPs...),
		}
	}

	return settings
}

func endpointAliases(serviceName string, cfg *types.ServiceNetworkConfig) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 1)
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}

	add(serviceName)
	if cfg != nil {
		for _, a := range cfg.Aliases {
			add(a)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func dockerIPAMConfig(cfg types.IPAMConfig) *network.IPAM {
	ipam := &network.IPAM{
		Driver: strings.TrimSpace(cfg.Driver),
	}
	if len(cfg.Config) > 0 {
		ipam.Config = make([]network.IPAMConfig, 0, len(cfg.Config))
		for _, p := range cfg.Config {
			if p == nil {
				continue
			}
			ipam.Config = append(ipam.Config, network.IPAMConfig{
				Subnet:     p.Subnet,
				IPRange:    p.IPRange,
				Gateway:    p.Gateway,
				AuxAddress: copyStringMap(p.AuxiliaryAddresses),
			})
		}
	}
	if ipam.Driver == "" && len(ipam.Config) == 0 {
		return nil
	}
	return ipam
}

func cloneBoolPtr(v *bool) *bool {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}

func copyStringMap[M ~map[string]string](src M) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func resolveResourceName(projectName, key, explicitName string, external bool) string {
	key = strings.TrimSpace(key)
	if name := strings.TrimSpace(explicitName); name != "" {
		return name
	}
	if external {
		return key
	}
	return resolveVolumeName(projectName, key)
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
	projectName := c.projectName()
	projectVolumes := c.projectVolumes()

	if len(projectVolumes) > 0 {
		return ensureProjectVolumes(ctx, dc, projectName, projectVolumes)
	}
	return ensureServiceVolumes(ctx, dc, projectName, c.Service.Volumes)
}

func ensureProjectVolumes(
	ctx context.Context,
	dc dockerAPI,
	projectName string,
	volumesMap types.Volumes,
) error {
	for volName, volCfg := range volumesMap {
		if bool(volCfg.External) {
			// External volumes must already exist; never create them.
			continue
		}

		resolved := resolveResourceName(projectName, volName, volCfg.Name, bool(volCfg.External))
		labels := make(map[string]string)
		for k, v := range volCfg.Labels {
			labels[k] = v
		}
		if projectName != "" {
			labels["com.docker.compose.project"] = projectName
		}
		labels["com.docker.compose.volume"] = volName

		createOpts := volume.CreateOptions{
			Name:       resolved,
			Driver:     strings.TrimSpace(volCfg.Driver),
			DriverOpts: copyStringMap(volCfg.DriverOpts),
			Labels:     labels,
		}
		if err := createVolumeIdempotent(ctx, dc, createOpts); err != nil {
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
		if err := createVolumeIdempotent(
			ctx,
			dc,
			volume.CreateOptions{Name: resolved},
		); err != nil {
			return err
		}
	}
	return nil
}

func createVolumeIdempotent(
	ctx context.Context,
	dc dockerAPI,
	createOpts volume.CreateOptions,
) error {
	_, err := dc.VolumeCreate(ctx, createOpts)
	if err != nil {
		if isAlreadyExistsErr(err) {
			return nil
		}
		return fmt.Errorf("failed to create volume %q: %w", createOpts.Name, err)
	}
	return nil
}

func resolveVolumeSource(projectName, volumeSource string, projectVolumes types.Volumes) string {
	volumeSource = strings.TrimSpace(volumeSource)
	if volumeSource == "" {
		return ""
	}
	if cfg, ok := projectVolumes[volumeSource]; ok {
		return resolveResourceName(projectName, volumeSource, cfg.Name, bool(cfg.External))
	}
	return resolveVolumeName(projectName, volumeSource)
}

func (c *Cmd) projectVolumes() types.Volumes {
	if c.service == nil || c.service.project == nil {
		return nil
	}
	return c.service.project.Volumes
}

func (c *Cmd) projectNetworks() types.Networks {
	if c.service == nil || c.service.project == nil {
		return nil
	}
	return c.service.project.Networks
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
