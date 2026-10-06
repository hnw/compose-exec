package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

// Each fixture is fed to the real Compose CLI and to compose-exec. The fake
// Engine records the wire request and deliberately rejects creation, so this
// test needs neither a daemon nor images and cannot create Docker resources.
var mountFixtures = map[string]string{
	"bind-short":              "volumes: [./data:/target]",
	"bind-short-ro":           "volumes: [./data:/target:ro]",
	"bind-shared":             "volumes: [./data:/target:z]",
	"bind-private":            "volumes: ['./data:/target:ro,Z']",
	"bind-long":               "volumes: [{type: bind, source: ./data, target: /target}]",
	"bind-no-create":          "volumes: [{type: bind, source: ./data, target: /target, bind: {create_host_path: false}}]",
	"bind-create":             "volumes: [{type: bind, source: ./data, target: /target, read_only: true, bind: {create_host_path: true}}]",
	"bind-propagation":        "volumes: [{type: bind, source: ./data, target: /target, bind: {propagation: rslave}}]",
	"bind-recursive-disabled": "volumes: [{type: bind, source: ./data, target: /target, bind: {recursive: disabled}}]",
	"bind-recursive-writable": "volumes: [{type: bind, source: ./data, target: /target, read_only: true, bind: {recursive: writable}}]",
	"bind-recursive-readonly": "volumes: [{type: bind, source: ./data, target: /target, read_only: true, bind: {recursive: readonly}}]",
	"bind-recursive-enabled":  "volumes: [{type: bind, source: ./data, target: /target, bind: {recursive: enabled}}]",
	"volume-short":            "volumes: [cache:/target]",
	"volume-long":             "volumes: [{type: volume, source: cache, target: /target, read_only: true}]",
	"volume-nocopy":           "volumes: [{type: volume, source: cache, target: /target, volume: {nocopy: true}}]",
	"volume-subpath":          "volumes: [{type: volume, source: cache, target: /target, volume: {subpath: sub, nocopy: true}}]",
	"volume-labels":           "volumes: [{type: volume, source: cache, target: /target, volume: {labels: {purpose: test}}}]",
	"anonymous-short":         "volumes: [/target]",
	"anonymous-long":          "volumes: [{type: volume, target: /target, read_only: true, volume: {nocopy: true}}]",
	"tmpfs-short":             "tmpfs: [/target]",
	"tmpfs-options":           "tmpfs: ['/target:size=1m,mode=1777']",
	"tmpfs-long":              "volumes: [{type: tmpfs, target: /target}]",
	"tmpfs-long-options":      "volumes: [{type: tmpfs, target: /target, read_only: true, tmpfs: {size: 1m, mode: 01777}}]",
	"bind-long-Z":             "volumes: [{type: bind, source: ./data, target: /target, bind: {selinux: Z, create_host_path: true}}]",
	"bind-create-propagation": "volumes: [{type: bind, source: ./data, target: /target, read_only: true, bind: {create_host_path: true, propagation: rshared}}]",
	"bind-consistency":        "volumes: [{type: bind, source: ./data, target: /target, consistency: cached, bind: {create_host_path: false}}]",
	"anonymous-labels":        "volumes: [{type: volume, target: /target, volume: {labels: {purpose: test}}}]",
	"mixed":                   "volumes: [./data:/bind, cache:/volume, /anonymous, {type: tmpfs, target: /memory, tmpfs: {size: 1m}}]\n    tmpfs: ['/tmp:size=1m,mode=1777']",
	"volume-custom-name":      "volumes: [custom:/target]",
	"volume-external":         "volumes: [shared:/target:ro]",
	"volume-top-labels":       "volumes: [labeled:/target]",
}

// Decode mount objects without SDK structs so new/unknown API fields are
// compared too, rather than silently discarded by JSON decoding.
type mountRequest struct {
	Binds  []string
	Mounts []map[string]any
	Tmpfs  map[string]string
}

func normalizeMountRequest(h *mountRequest, dir string) mountRequest {
	r := *h
	for i := range r.Binds {
		r.Binds[i] = strings.ReplaceAll(r.Binds[i], dir, "$PROJECT")
	}
	for i := range r.Mounts {
		if source, ok := r.Mounts[i]["Source"].(string); ok {
			r.Mounts[i]["Source"] = strings.ReplaceAll(source, dir, "$PROJECT")
		}
	}
	sort.Strings(r.Binds)
	sort.Slice(
		r.Mounts,
		func(i, j int) bool { return fmt.Sprint(r.Mounts[i]["Target"]) < fmt.Sprint(r.Mounts[j]["Target"]) },
	)
	if len(r.Binds) == 0 {
		r.Binds = nil
	}
	if len(r.Mounts) == 0 {
		r.Mounts = nil
	}
	if len(r.Tmpfs) == 0 {
		r.Tmpfs = nil
	}
	return r
}

func captureEngine(t *testing.T) (*httptest.Server, <-chan *mountRequest) {
	t.Helper()
	captured := make(chan *mountRequest, 2)
	versionPrefix := regexp.MustCompile(`^/v[0-9.]+`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := versionPrefix.ReplaceAllString(r.URL.Path, "")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case path == "/_ping":
			w.Header().Set("API-Version", "1.53")
			w.Header().Set("OSType", "linux")
			_, _ = fmt.Fprint(w, "OK")
		case path == "/version":
			_, _ = fmt.Fprint(
				w,
				`{"ApiVersion":"1.53","MinAPIVersion":"1.44","Version":"29.2.1","Os":"linux","Arch":"arm64"}`,
			)
		case path == "/info":
			_, _ = fmt.Fprint(w, `{"OSType":"linux","Architecture":"aarch64"}`)
		case path == "/containers/json":
			_, _ = fmt.Fprint(w, `[]`)
		case strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
			_, _ = fmt.Fprint(
				w,
				`{"Id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Config":{},"Os":"linux","Architecture":"arm64"}`,
			)

		case strings.HasPrefix(path, "/volumes/"):
			captureVolumeResponse(t, w, r, path)

		case path == "/containers/create":
			var req struct{ HostConfig *mountRequest }
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode create: %v", err)
			} else if req.HostConfig == nil {
				t.Error("create request has no HostConfig")
			} else {
				captured <- req.HostConfig
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"message":"capture complete"}`)
		default:
			t.Errorf("unexpected Engine request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"message":"unexpected request"}`)
		}
	}))
	t.Cleanup(server.Close)
	return server, captured
}

func captureVolumeResponse(t *testing.T, w http.ResponseWriter, r *http.Request, path string) {
	t.Helper()
	if path == "/volumes/create" {
		var request struct {
			Name   string
			Labels map[string]string
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode volume create: %v", err)
		}
		if err := json.NewEncoder(w).Encode(request); err != nil {
			t.Errorf("volume response: %v", err)
		}
		return
	}
	name := strings.TrimPrefix(path, "/volumes/")
	key := map[string]string{"mountprobe_cache": "cache", "explicit_cache": "custom", "existing_cache": "shared", "mountprobe_labeled": "labeled"}[name]
	if key == "" {
		t.Errorf("unexpected volume %q", name)
	}
	response := map[string]any{
		"Name":   name,
		"Driver": "local",
		"Labels": map[string]string{
			"com.docker.compose.project": "mountprobe",
			"com.docker.compose.volume":  key,
		},
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		t.Errorf("volume response: %v", err)
	}
}

//nolint:gocyclo // Exercises capture, replay and wire comparison for each fixture.
func TestMountSerialization(t *testing.T) {
	live := os.Getenv("COMPOSE_EXEC_MOUNT_LIVE") != ""
	update := os.Getenv("COMPOSE_EXEC_UPDATE_MOUNTS") != ""
	if update && !live {
		t.Fatal("updating captures requires COMPOSE_EXEC_MOUNT_LIVE=1")
	}
	for name, service := range mountFixtures {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			yaml := "name: mountprobe\nservices:\n  probe:\n    image: busybox:latest\n    network_mode: none\n    " + service + "\nvolumes:\n  cache: {}\n  custom: {name: explicit_cache}\n  shared: {external: true, name: existing_cache}\n  labeled: {labels: {purpose: test}}\n"
			if err := os.WriteFile(
				filepath.Join(dir, "compose.yaml"),
				[]byte(yaml),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", "mounts", name+".json")
			var want mountRequest
			if live {
				server, captured := captureEngine(t)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				// #nosec G204 -- Arguments come exclusively from these fixtures and the local capture server.
				cmd := exec.CommandContext(
					ctx,
					"docker",
					"--host",
					strings.Replace(server.URL, "http://", "tcp://", 1),
					"compose",
					"-f",
					filepath.Join(dir, "compose.yaml"),
					"run",
					"--no-deps",
					"--pull",
					"never",
					"probe",
					"true",
				)
				cmd.Env = append(
					os.Environ(),
					"DOCKER_CONTEXT=",
					"DOCKER_HOST=",
					"DOCKER_TLS_VERIFY=",
					"DOCKER_API_VERSION=1.53",
				)
				output, err := cmd.CombinedOutput()
				select {
				case h := <-captured:
					want = normalizeMountRequest(h, dir)
				default:
					t.Fatalf("Compose did not send create: %v\n%s", err, output)
				}
				if update {
					data, err := json.MarshalIndent(want, "", "  ")
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(golden, append(data, '\n'), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				// #nosec G304 -- Golden path is derived from the fixed fixture names.
				data, err := os.ReadFile(golden)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(data, &want); err != nil {
					t.Fatal(err)
				}
			}
			project, err := LoadProject(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			server, captured := captureEngine(t)
			cli, err := client.New(
				client.WithHost(strings.Replace(server.URL, "http://", "tcp://", 1)),
				client.WithAPIVersion("1.53"),
			)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cli.Close() }()
			cmd := project.Command("probe", "true")
			cmd.docker = cli
			err = cmd.Start()
			select {
			case h := <-captured:
				got := normalizeMountRequest(h, dir)
				a, _ := json.MarshalIndent(got, "", "  ")
				b, _ := json.MarshalIndent(want, "", "  ")
				if string(a) != string(b) {
					t.Errorf("mount request differs from Docker Compose\ngot: %s\nwant: %s", a, b)
				}
			default:
				t.Fatalf("compose-exec did not send create: %v", err)
			}
		})
	}
}
