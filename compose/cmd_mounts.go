package compose

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

func bindOptions(b *types.ServiceVolumeBind) *mount.BindOptions {
	if b == nil {
		return nil
	}
	opts := &mount.BindOptions{
		Propagation:      mount.Propagation(b.Propagation),
		CreateMountpoint: bool(b.CreateHostPath),
	}
	switch b.Recursive {
	case "disabled":
		opts.NonRecursive = true
	case "writable":
		opts.ReadOnlyNonRecursive = true
	case "readonly":
		opts.ReadOnlyForceRecursive = true
	}
	return opts
}

func volumeOptions(v *types.ServiceVolumeVolume) *mount.VolumeOptions {
	if v == nil {
		return nil
	}
	return &mount.VolumeOptions{NoCopy: v.NoCopy, Subpath: v.Subpath, Labels: v.Labels}
}

// Match Docker Compose's buildContainerVolumes selection rules. A bind that
// must not create its source, or specifies propagation/recursion, uses Mounts.
// Named volumes use Binds unless labels, subpath or nocopy require Mounts.
// Anonymous volumes always use Mounts.
func (c *Cmd) applyMounts(h *container.HostConfig, mounts []mount.Mount) {
	// Compose deduplicates mounts by target; later volumes win.
	byTarget := make(map[string]mount.Mount)
	volumes := make(map[string]types.ServiceVolumeConfig)
	for _, v := range c.Service.Volumes {
		volumes[v.Target] = v
	}
	for _, m := range mounts {
		byTarget[m.Target] = m
	}
	// Compose iterates a map; stable ordering keeps our requests reproducible.
	targets := make([]string, 0, len(byTarget))
	for target := range byTarget {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	for _, target := range targets {
		m := byTarget[target]
		v, declared := volumes[target]
		legacy := declared && c.useBindString(v, m)
		if legacy {
			v.Source = m.Source
			h.Binds = append(h.Binds, v.String())
		} else {
			h.Mounts = append(h.Mounts, m)
		}
	}
}

func (c *Cmd) useBindString(v types.ServiceVolumeConfig, m mount.Mount) bool {
	switch m.Type {
	case mount.TypeBind:
		return v.Bind == nil ||
			(bool(v.Bind.CreateHostPath) && v.Bind.Propagation == "" && v.Bind.Recursive == "")
	case mount.TypeVolume:
		_, named := c.projectVolumes()[v.Source]
		return named && m.Source != "" &&
			(v.Volume == nil || (len(v.Volume.Labels) == 0 && v.Volume.Subpath == "" && !v.Volume.NoCopy))
	default:
		return false
	}
}

func serviceMounts(
	svc types.ServiceConfig,
	baseDir, projectName string,
	projectVolumes types.Volumes,
) ([]mount.Mount, error) {
	var out []mount.Mount
	for _, v := range svc.Volumes {
		m, err := serviceMount(v, baseDir, projectName, projectVolumes)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func serviceMount(
	v types.ServiceVolumeConfig,
	baseDir, projectName string,
	projectVolumes types.Volumes,
) (mount.Mount, error) {
	m := mount.Mount{
		Type:        mount.Type(v.Type),
		Source:      v.Source,
		Target:      v.Target,
		ReadOnly:    v.ReadOnly,
		Consistency: mount.Consistency(v.Consistency),
	}
	switch v.Type {
	case "", types.VolumeTypeBind:
		if strings.TrimSpace(v.Source) == "" {
			return m, errors.New("compose: bind mount source is required")
		}
		if !filepath.IsAbs(m.Source) {
			m.Source = filepath.Join(baseDir, m.Source)
		}
		source, err := filepath.Abs(m.Source)
		if err != nil {
			return m, err
		}
		m.Type = mount.TypeBind
		m.Source = source
		m.BindOptions = bindOptions(v.Bind)
	case types.VolumeTypeVolume:
		if m.Source != "" {
			m.Source = resolveVolumeSource(projectName, m.Source, projectVolumes)
		}
		m.VolumeOptions = volumeOptions(v.Volume)
	case types.VolumeTypeTmpfs:
		if strings.TrimSpace(v.Target) == "" {
			return m, errors.New("compose: tmpfs mount target is required")
		}
		if v.Tmpfs != nil {
			m.TmpfsOptions = &mount.TmpfsOptions{
				SizeBytes: int64(v.Tmpfs.Size),
				Mode:      os.FileMode(v.Tmpfs.Mode),
			}
		}
	default:
		return m, fmt.Errorf(
			"compose: unsupported volume type %q (supported: bind, volume, tmpfs)",
			v.Type,
		)
	}
	return m, nil
}
