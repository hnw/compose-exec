package compose

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/docker/cli/cli/config"
	cliconfigfile "github.com/docker/cli/cli/config/configfile"
	"github.com/docker/cli/cli/context"
	dockercontext "github.com/docker/cli/cli/context/docker"
	"github.com/docker/cli/cli/context/store"
	"github.com/docker/cli/opts"
	"github.com/docker/docker/client"
)

const (
	defaultContextName = "default"
	// envEnableTLS enables TLS for client connections. For backward
	// compatibility it can only be used to enable TLS, never to disable it.
	envEnableTLS = "DOCKER_TLS"
	// envContextOverride selects a named context from the context store.
	envContextOverride = "DOCKER_CONTEXT"
)

// Default file names within the cert path, matching the documented Docker
// CLI on-disk layout.
const (
	caFileName   = "ca.pem"
	certFileName = "cert.pem"
	keyFileName  = "key.pem"
)

// dockerClientOpts resolves the Docker daemon endpoint following the same
// precedence as the Docker CLI, and returns the client options to construct
// an API client with:
//
//  1. DOCKER_HOST (non-empty): the "default" context built from the legacy
//     DOCKER_HOST / DOCKER_TLS / DOCKER_TLS_VERIFY / DOCKER_CERT_PATH env vars.
//  2. DOCKER_CONTEXT (non-empty): the named context with that name.
//  3. "currentContext" in the CLI config file (config.Dir()/config.json).
//  4. Otherwise: the "default" context.
func dockerClientOpts() ([]client.Opt, error) {
	cfg, err := config.Load("")
	if err != nil {
		return nil, fmt.Errorf("failed to load Docker config: %w", err)
	}
	ep, err := dockerEndpoint(resolveContextName(cfg))
	if err != nil {
		return nil, fmt.Errorf("unable to resolve docker endpoint: %w", err)
	}
	return ep.ClientOpts()
}

// resolveContextName returns the current context name, based on environment
// variables and the CLI config file, in the same order of preference as the
// Docker CLI. Like the CLI, it does not validate whether the named context
// exists; errors may occur when trying to resolve its endpoint.
func resolveContextName(cfg *cliconfigfile.ConfigFile) string {
	if os.Getenv(client.EnvOverrideHost) != "" {
		return defaultContextName
	}
	if name := os.Getenv(envContextOverride); name != "" {
		return name
	}
	if cfg != nil && cfg.CurrentContext != "" {
		return cfg.CurrentContext
	}
	return defaultContextName
}

// dockerEndpoint resolves the Docker endpoint for the given context name.
func dockerEndpoint(contextName string) (dockercontext.Endpoint, error) {
	if contextName == defaultContextName {
		return defaultContextEndpoint()
	}
	return namedContextEndpoint(contextName)
}

// defaultContextEndpoint builds the endpoint for the "default" context from
// the legacy environment variables, mirroring the Docker CLI behavior:
//
//   - DOCKER_HOST is parsed with opts.ParseHost, which fills in the platform
//     default (unix:///var/run/docker.sock or the Windows named pipe) and
//     normalizes tcp:// addresses.
//   - When TLS is enabled (DOCKER_TLS or DOCKER_TLS_VERIFY), ca.pem is always
//     loaded from DOCKER_CERT_PATH (or the config dir), while cert.pem and
//     key.pem are ignored when the files do not exist. A missing ca.pem is an
//     error.
//   - TLS verification is skipped unless DOCKER_TLS_VERIFY is set.
func defaultContextEndpoint() (dockercontext.Endpoint, error) {
	tlsVerify := os.Getenv(client.EnvTLSVerify) != ""
	tlsEnabled := os.Getenv(envEnableTLS) != "" || tlsVerify

	host, err := opts.ParseHost(tlsEnabled, os.Getenv(client.EnvOverrideHost))
	if err != nil {
		return dockercontext.Endpoint{}, err
	}

	ep := dockercontext.Endpoint{
		EndpointMeta: dockercontext.EndpointMeta{
			Host:          host,
			SkipTLSVerify: tlsEnabled && !tlsVerify,
		},
	}
	if !tlsEnabled {
		return ep, nil
	}

	certPath := os.Getenv(client.EnvOverrideCertPath)
	if certPath == "" {
		certPath = config.Dir()
	}
	certFile := filepath.Join(certPath, certFileName)
	keyFile := filepath.Join(certPath, keyFileName)
	if _, statErr := os.Stat(certFile); os.IsNotExist(statErr) {
		certFile = ""
	}
	if _, statErr := os.Stat(keyFile); os.IsNotExist(statErr) {
		keyFile = ""
	}
	tlsData, err := context.TLSDataFromFiles(
		filepath.Join(certPath, caFileName), certFile, keyFile,
	)
	if err != nil {
		return dockercontext.Endpoint{}, err
	}
	ep.TLSData = tlsData
	return ep, nil
}

// namedContextEndpoint resolves the Docker endpoint for a named context from
// the context store, loading its TLS material if present. Protocol handling
// (unix / tcp / npipe / fd / ssh), TLS, and API version negotiation are
// delegated to the upstream endpoint implementation via ClientOpts().
func namedContextEndpoint(name string) (dockercontext.Endpoint, error) {
	s := store.New(config.ContextStoreDir(), store.NewConfig(
		nil,
		store.EndpointTypeGetter(
			dockercontext.DockerEndpoint,
			func() any { return &dockercontext.EndpointMeta{} },
		),
	))
	meta, err := s.GetMetadata(name)
	if err != nil {
		return dockercontext.Endpoint{}, err
	}
	epMeta, err := dockercontext.EndpointFromContext(meta)
	if err != nil {
		return dockercontext.Endpoint{}, err
	}
	return dockercontext.WithTLSData(s, name, epMeta)
}
