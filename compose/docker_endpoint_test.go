package compose

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/docker/cli/cli/config"
	dockercontext "github.com/docker/cli/cli/context/docker"
	"github.com/docker/cli/cli/context/store"
	"github.com/docker/docker/client"
)

// useTempConfigDir points the Docker CLI config directory to a temporary
// directory for the duration of the test and restores the previous value.
func useTempConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := config.Dir()
	config.SetDir(dir)
	t.Cleanup(func() { config.SetDir(orig) })
	return dir
}

// writeConfigFile writes a config.json with the given currentContext.
func writeConfigFile(t *testing.T, dir, currentContext string) {
	t.Helper()
	content := "{}"
	if currentContext != "" {
		content = `{"currentContext": "` + currentContext + `"}`
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveContextName(t *testing.T) {
	tests := []struct {
		name          string
		dockerHost    string
		dockerContext string
		configContext string
		expected      string
	}{
		{
			name:          "DOCKER_HOST wins over everything",
			dockerHost:    "tcp://localhost:2375",
			dockerContext: "ctx-env",
			configContext: "ctx-config",
			expected:      defaultContextName,
		},
		{
			name:          "DOCKER_CONTEXT wins over config",
			dockerContext: "ctx-env",
			configContext: "ctx-config",
			expected:      "ctx-env",
		},
		{
			name:          "config currentContext is used when no env vars",
			configContext: "ctx-config",
			expected:      "ctx-config",
		},
		{
			name:     "default when nothing is set",
			expected: defaultContextName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(client.EnvOverrideHost, tt.dockerHost)
			t.Setenv(envContextOverride, tt.dockerContext)

			dir := useTempConfigDir(t)
			writeConfigFile(t, dir, tt.configContext)

			cfg, err := config.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got := resolveContextName(cfg); got != tt.expected {
				t.Errorf("resolveContextName() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// writeCertFiles writes dummy cert files. Endpoint resolution only reads the
// files; TLS material content is validated by the upstream endpoint
// implementation when a TLS config is built.
func writeCertFiles(t *testing.T, dir string, files ...string) {
	t.Helper()
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"-data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDefaultContextEndpoint(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		certFiles   []string
		wantHost    string
		wantTLSData bool
		wantSkipTLS bool
		wantErr     bool
	}{
		{
			name:     "no env: platform default unix socket",
			wantHost: "unix:///var/run/docker.sock",
		},
		{
			name:     "unix socket host is preserved",
			env:      map[string]string{"DOCKER_HOST": "unix:///custom/docker.sock"},
			wantHost: "unix:///custom/docker.sock",
		},
		{
			name:     "tcp host gets default port",
			env:      map[string]string{"DOCKER_HOST": "tcp://localhost"},
			wantHost: "tcp://localhost:2375",
		},
		{
			name:    "invalid host scheme",
			env:     map[string]string{"DOCKER_HOST": "foo://bar"},
			wantErr: true,
		},
		{
			name: "DOCKER_TLS enables TLS without verification",
			env: map[string]string{
				"DOCKER_HOST": "tcp://localhost:2376",
				"DOCKER_TLS":  "1",
			},
			certFiles:   []string{caFileName, certFileName, keyFileName},
			wantHost:    "tcp://localhost:2376",
			wantTLSData: true,
			wantSkipTLS: true,
		},
		{
			name: "DOCKER_TLS_VERIFY alone enables TLS with verification",
			env: map[string]string{
				"DOCKER_HOST":       "tcp://localhost:2376",
				"DOCKER_TLS_VERIFY": "1",
			},
			certFiles:   []string{caFileName},
			wantHost:    "tcp://localhost:2376",
			wantTLSData: true,
		},
		{
			name: "DOCKER_TLS_VERIFY disables SkipTLSVerify of DOCKER_TLS",
			env: map[string]string{
				"DOCKER_HOST":       "tcp://localhost:2376",
				"DOCKER_TLS":        "1",
				"DOCKER_TLS_VERIFY": "1",
			},
			certFiles:   []string{caFileName},
			wantHost:    "tcp://localhost:2376",
			wantTLSData: true,
		},
		{
			name: "cert and key files are optional",
			env: map[string]string{
				"DOCKER_HOST": "tcp://localhost:2376",
				"DOCKER_TLS":  "1",
			},
			certFiles:   []string{caFileName},
			wantHost:    "tcp://localhost:2376",
			wantTLSData: true,
			wantSkipTLS: true,
		},
		{
			name: "missing ca.pem is an error when TLS is enabled",
			env: map[string]string{
				"DOCKER_HOST": "tcp://localhost:2376",
				"DOCKER_TLS":  "1",
			},
			wantErr: true,
		},
		{
			name: "TLS material is loaded even for unix sockets",
			env: map[string]string{
				"DOCKER_HOST": "unix:///custom/docker.sock",
				"DOCKER_TLS":  "1",
			},
			certFiles:   []string{caFileName},
			wantHost:    "unix:///custom/docker.sock",
			wantTLSData: true,
			wantSkipTLS: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantHost == "unix:///var/run/docker.sock" && runtime.GOOS == "windows" {
				t.Skip("platform default differs on windows")
			}
			t.Setenv("DOCKER_HOST", "")
			t.Setenv("DOCKER_TLS", "")
			t.Setenv("DOCKER_TLS_VERIFY", "")
			t.Setenv("DOCKER_CERT_PATH", "")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			certPath := useTempConfigDir(t)
			if tt.certFiles != nil {
				writeCertFiles(t, certPath, tt.certFiles...)
			}

			ep, err := defaultContextEndpoint()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("defaultContextEndpoint() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("defaultContextEndpoint() error = %v", err)
			}
			if ep.Host != tt.wantHost {
				t.Errorf("Host = %q, want %q", ep.Host, tt.wantHost)
			}
			if got := ep.TLSData != nil; got != tt.wantTLSData {
				t.Errorf("TLSData present = %v, want %v", got, tt.wantTLSData)
			}
			if ep.SkipTLSVerify != tt.wantSkipTLS {
				t.Errorf("SkipTLSVerify = %v, want %v", ep.SkipTLSVerify, tt.wantSkipTLS)
			}
		})
	}
}

func TestDefaultContextEndpointCertPathEnv(t *testing.T) {
	t.Setenv("DOCKER_TLS", "1")
	t.Setenv("DOCKER_HOST", "tcp://localhost:2376")
	t.Setenv("DOCKER_TLS_VERIFY", "")

	configDir := useTempConfigDir(t)
	certPath := t.TempDir()
	t.Setenv("DOCKER_CERT_PATH", certPath)
	writeCertFiles(t, certPath, caFileName, certFileName, keyFileName)
	writeConfigFile(t, configDir, "")

	ep, err := defaultContextEndpoint()
	if err != nil {
		t.Fatalf("defaultContextEndpoint() error = %v", err)
	}
	if ep.TLSData == nil || string(ep.TLSData.CA) != caFileName+"-data" {
		t.Errorf("TLSData.CA = %q, want %q", ep.TLSData, caFileName+"-data")
	}
}

func TestNamedContextEndpoint(t *testing.T) {
	configDir := useTempConfigDir(t)

	s := store.New(config.ContextStoreDir(), store.NewConfig(
		nil,
		store.EndpointTypeGetter(
			dockercontext.DockerEndpoint,
			func() any { return &dockercontext.EndpointMeta{} },
		),
	))
	err := s.CreateOrUpdate(store.Metadata{
		Name:     "colima",
		Metadata: map[string]any{"Description": "test context"},
		Endpoints: map[string]any{
			dockercontext.DockerEndpoint: dockercontext.EndpointMeta{
				Host: "unix:///tmp/colima.sock",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resetErr := s.ResetEndpointTLSMaterial(
		"colima",
		dockercontext.DockerEndpoint,
		&store.EndpointTLSData{
			Files: map[string][]byte{caFileName: []byte("colima-ca")},
		},
	); resetErr != nil {
		t.Fatal(resetErr)
	}
	writeConfigFile(t, configDir, "colima")

	ep, err := namedContextEndpoint("colima")
	if err != nil {
		t.Fatalf("namedContextEndpoint() error = %v", err)
	}
	if ep.Host != "unix:///tmp/colima.sock" {
		t.Errorf("Host = %q, want %q", ep.Host, "unix:///tmp/colima.sock")
	}
	if ep.TLSData == nil || string(ep.TLSData.CA) != "colima-ca" {
		t.Errorf("TLSData.CA = %v, want colima-ca", ep.TLSData)
	}

	if _, err := namedContextEndpoint("missing"); err == nil {
		t.Error("namedContextEndpoint(missing) error = nil, want error")
	}
}

func TestDockerClientOpts(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_TLS", "")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_CONTEXT", "")

	t.Run("DOCKER_HOST", func(t *testing.T) {
		t.Setenv("DOCKER_HOST", "unix:///custom/docker.sock")
		cli, err := client.NewClientWithOpts(mustClientOpts(t)...)
		if err != nil {
			t.Fatal(err)
		}
		if got := cli.DaemonHost(); got != "unix:///custom/docker.sock" {
			t.Errorf("Endpoint() = %q, want %q", got, "unix:///custom/docker.sock")
		}
	})

	t.Run("named context from config", func(t *testing.T) {
		configDir := useTempConfigDir(t)
		s := store.New(config.ContextStoreDir(), store.NewConfig(
			nil,
			store.EndpointTypeGetter(
				dockercontext.DockerEndpoint,
				func() any { return &dockercontext.EndpointMeta{} },
			),
		))
		err := s.CreateOrUpdate(store.Metadata{
			Name: "myctx",
			Endpoints: map[string]any{
				dockercontext.DockerEndpoint: dockercontext.EndpointMeta{
					Host: "unix:///tmp/from-context.sock",
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		writeConfigFile(t, configDir, "myctx")

		cli, err := client.NewClientWithOpts(mustClientOpts(t)...)
		if err != nil {
			t.Fatal(err)
		}
		if got := cli.DaemonHost(); got != "unix:///tmp/from-context.sock" {
			t.Errorf("Endpoint() = %q, want %q", got, "unix:///tmp/from-context.sock")
		}
	})

	t.Run("malformed config.json is reported", func(t *testing.T) {
		configDir := useTempConfigDir(t)
		if err := os.WriteFile(
			filepath.Join(configDir, "config.json"),
			[]byte("{invalid"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := dockerClientOpts(); err == nil {
			t.Fatal("dockerClientOpts() error = nil, want error")
		}
	})
}

// writeValidCertificates writes a self-signed CA and a client certificate
// signed by it as ca.pem / cert.pem / key.pem. Unlike writeCertFiles, the
// resulting endpoint can be converted into an actual TLS configuration by
// Endpoint.ClientOpts(). It returns the parsed CA certificate and the leaf
// pair as tls.Certificate so tests can also serve the TLS server side.
func writeValidCertificates(t *testing.T, dir string) (*x509.Certificate, tls.Certificate) {
	t.Helper()
	notBefore := time.Now().Add(-time.Hour)
	notAfter := time.Now().Add(time.Hour)

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "compose-exec test CA"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(
		rand.Reader,
		&caTemplate,
		&caTemplate,
		&caKey.PublicKey,
		caKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "compose-exec test"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	leafDER, err := x509.CreateCertificate(
		rand.Reader,
		&leafTemplate,
		caCert,
		&leafKey.PublicKey,
		caKey,
	)
	if err != nil {
		t.Fatal(err)
	}

	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	leafKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER})
	files := map[string][]byte{
		caFileName:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		certFileName: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		keyFileName:  leafKeyPEM,
	}
	for name, data := range files {
		if writeErr := os.WriteFile(filepath.Join(dir, name), data, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	leafTLSCert, err := tls.X509KeyPair(files[certFileName], leafKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return caCert, leafTLSCert
}

// TestDockerClientOptsTLSEndToEnd verifies that the endpoint built from the
// legacy TLS env vars survives the full production path:
// defaultContextEndpoint -> Endpoint.ClientOpts -> client.NewClientWithOpts,
// by performing an actual TLS handshake against a local test server. The
// server requires a client certificate signed by the same CA, so a successful
// Ping proves that the CA, the client certificate, and TLS verification were
// all wired up correctly.
func TestDockerClientOptsTLSEndToEnd(t *testing.T) {
	certPath := useTempConfigDir(t)
	caCert, leafTLSCert := writeValidCertificates(t, certPath)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("API-Version", "1.48")
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewUnstartedServer(handler)
	caPool := x509.NewCertPool()
	caPool.AddCert(caCert)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{leafTLSCert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}
	srv.StartTLS()
	defer srv.Close()

	t.Setenv("DOCKER_HOST", "tcp://"+srv.Listener.Addr().String())
	t.Setenv("DOCKER_TLS", "")
	t.Setenv("DOCKER_TLS_VERIFY", "1")
	t.Setenv("DOCKER_CERT_PATH", "")

	cli, err := client.NewClientWithOpts(mustClientOpts(t)...)
	if err != nil {
		t.Fatalf("NewClientWithOpts: %v", err)
	}
	defer func() { _ = cli.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		t.Fatalf("Ping over TLS: %v", err)
	}
}

func mustClientOpts(t *testing.T) []client.Opt {
	t.Helper()
	opts, err := dockerClientOpts()
	if err != nil {
		t.Fatal(err)
	}
	return opts
}
