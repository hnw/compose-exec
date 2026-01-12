package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
)

// LoadProject loads a compose project from compose files within dir.
//
// If files is empty, it defaults to docker-compose.yml and docker-compose.override.yml
// (the latter only if it exists).
//
// Environment variable resolution follows compose-go behavior, including .env in dir.
func LoadProject(ctx context.Context, dir string, files ...string) (*types.Project, error) {
	if dir == "" {
		return nil, errors.New("dir is required")
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	configFiles := defaultComposeFiles(absDir, files)

	cd := types.ConfigDetails{
		WorkingDir: absDir,
		ConfigFiles: func() []types.ConfigFile {
			out := make([]types.ConfigFile, 0, len(configFiles))
			for _, f := range configFiles {
				out = append(out, types.ConfigFile{Filename: f})
			}
			return out
		}(),
		Environment: currentEnvMap(),
	}

	project, err := loader.LoadWithContext(ctx, cd, func(opts *loader.Options) {
		// Try loading without forcing a project name, so that 'name:' in YAML takes precedence.
		opts.SkipNormalization = false
		opts.Profiles = []string{"*"}
	})
	if err != nil {
		project, err = loader.LoadWithContext(ctx, cd, func(opts *loader.Options) {
			// If loading failed (likely due to missing project name in YAML),
			// fallback to using the directory name with standard normalization.
			opts.SkipNormalization = false
			opts.Profiles = []string{"*"}
			name := filepath.Base(absDir)
			opts.SetProjectName(name, true)
		})
	}

	if err != nil {
		return nil, err
	}
	return project, nil
}

func defaultComposeFiles(dir string, files []string) []string {
	if len(files) > 0 {
		out := make([]string, 0, len(files))
		for _, f := range files {
			if filepath.IsAbs(f) {
				out = append(out, f)
				continue
			}
			out = append(out, filepath.Join(dir, f))
		}
		return out
	}

	base := filepath.Join(dir, "docker-compose.yml")
	out := []string{base}
	override := filepath.Join(dir, "docker-compose.override.yml")
	if _, err := os.Stat(override); err == nil {
		out = append(out, override)
	}
	return out
}

func currentEnvMap() map[string]string {
	out := make(map[string]string)
	for _, kv := range os.Environ() {
		k, v, ok := splitEnv(kv)
		if !ok {
			continue
		}
		out[k] = v
	}
	return out
}
