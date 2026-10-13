package compose

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

func TestImagePullRequired(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		policy   string
		missing  bool
		age      time.Duration
		zero     bool
		wantPull bool
		wantErr  string
	}{
		{name: "unspecified local"},
		{name: "unspecified missing", missing: true, wantPull: true},
		{name: "missing local", policy: "missing"},
		{name: "missing absent", policy: "missing", missing: true, wantPull: true},
		{name: "alias local", policy: "if_not_present"},
		{name: "alias absent", policy: "if_not_present", missing: true, wantPull: true},
		{name: "always local", policy: "always", wantPull: true},
		{name: "always absent", policy: "always", missing: true, wantPull: true},
		{name: "never local", policy: "never"},
		{name: "never absent", policy: "never", missing: true, wantErr: "unavailable"},
		{name: "daily fresh", policy: "daily", age: 23 * time.Hour},
		{name: "daily expired", policy: "daily", age: 25 * time.Hour, wantPull: true},
		{name: "daily absent", policy: "daily", missing: true, wantPull: true},
		{name: "weekly fresh", policy: "weekly", age: 6 * 24 * time.Hour},
		{name: "weekly expired", policy: "weekly", age: 8 * 24 * time.Hour, wantPull: true},
		{name: "weekly absent", policy: "weekly", missing: true, wantPull: true},
		{
			name:   "custom before boundary",
			policy: "every_1h30m",
			age:    90*time.Minute - time.Nanosecond,
		},
		{name: "custom at boundary", policy: "every_1h30m", age: 90 * time.Minute},
		{
			name:     "custom after boundary",
			policy:   "every_1h30m",
			age:      90*time.Minute + time.Nanosecond,
			wantPull: true,
		},
		{name: "custom absent", policy: "every_1h30m", missing: true, wantPull: true},
		{name: "custom compose duration", policy: "every_2d", age: 49 * time.Hour, wantPull: true},
		{name: "zero last tag", policy: "daily", zero: true, wantPull: true},
		{name: "invalid policy", policy: "occasionally", wantErr: "invalid pull_policy"},
		{name: "invalid duration", policy: "every_nonsense", wantErr: "invalid pull_policy"},
		{name: "empty duration", policy: "every_", wantErr: "invalid pull_policy"},
		{name: "build unsupported", policy: "build", wantErr: "not supported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lastTag := now.Add(-tt.age)
			if tt.zero {
				lastTag = time.Time{}
			}
			fd := &fakeDocker{imageInspectResp: client.ImageInspectResult{
				InspectResponse: image.InspectResponse{
					Metadata: image.Metadata{LastTagTime: lastTag},
					// A recent creation time must not replace a zero last-tag time.
					Created: now.Format(time.RFC3339),
				},
			}}
			if tt.missing {
				fd.imageInspectErr = cerrdefs.ErrNotFound
			}
			svc := types.ServiceConfig{Image: "tool:latest", PullPolicy: tt.policy}
			required, err := imagePullRequired(t.Context(), fd, svc, now)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err=%v want containing %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if required != tt.wantPull {
				t.Fatalf("required=%v want=%v", required, tt.wantPull)
			}
		})
	}
}

type pullRoundTripper func(*http.Request) (*http.Response, error)

func (f pullRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// Use the real SDK stream implementation without a daemon, registry or socket.
func sdkPullResponse(t *testing.T, body io.ReadCloser) client.ImagePullResponse {
	t.Helper()
	dc, err := client.New(
		client.WithHost("http://docker.test"),
		client.WithAPIVersion("1.53"),
		client.WithHTTPClient(
			&http.Client{
				Transport: pullRoundTripper(func(req *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK, Header: http.Header{}, Body: body, Request: req,
					}, nil
				}),
			},
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dc.Close() })
	response, err := dc.ImagePull(t.Context(), "tool:latest", client.ImagePullOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

type failingPullReader struct{ err error }

func (r failingPullReader) Read([]byte) (int, error) { return 0, r.err }

type trackedPullBody struct {
	io.ReadCloser
	closed bool
}

func (b *trackedPullBody) Close() error {
	b.closed = true
	return b.ReadCloser.Close()
}

func TestPullImageStream(t *testing.T) {
	transportErr := errors.New("stream transport failed")
	tests := []struct {
		name    string
		stream  string
		readErr error
		wantErr string
	}{
		{name: "success", stream: "{\"status\":\"pulling\"}\n{\"status\":\"complete\"}\n"},
		{
			name:    "embedded error",
			stream:  "{\"status\":\"pulling\"}\n{\"errorDetail\":{\"message\":\"denied\"},\"error\":\"denied\"}",
			wantErr: "denied",
		},
		{name: "truncated response", stream: "{", wantErr: "unexpected EOF"},
		{name: "transport error", readErr: transportErr, wantErr: transportErr.Error()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reader io.Reader = strings.NewReader(tt.stream)
			if tt.readErr != nil {
				reader = failingPullReader{tt.readErr}
			}
			body := &trackedPullBody{ReadCloser: io.NopCloser(reader)}
			fd := &fakeDocker{
				imageInspectErr:   cerrdefs.ErrNotFound,
				imagePullResponse: sdkPullResponse(t, body),
			}
			err := pullImage(t.Context(), fd, types.ServiceConfig{Image: "tool:latest"})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error=%v want containing %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !body.closed || fd.imagePullCalls != 1 {
				t.Fatalf("closed=%v pull calls=%d", body.closed, fd.imagePullCalls)
			}
		})
	}
}

func TestPullFailurePreventsCreate(t *testing.T) {
	pullErr := errors.New("pull API failed")
	fd := &fakeDocker{imagePullErr: pullErr}
	cmd := &Cmd{
		Service: types.ServiceConfig{Image: "tool:latest", PullPolicy: "always"},
		docker:  fd,
	}
	if err := cmd.Start(); !errors.Is(err, pullErr) {
		t.Fatalf("Start error=%v want=%v", err, pullErr)
	}
	if fd.imagePullCalls != 1 || fd.createCalls != 0 {
		t.Fatalf("pull calls=%d create calls=%d", fd.imagePullCalls, fd.createCalls)
	}
}

func TestPullCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	project := &Project{Services: types.Services{"tool": {
		Name: "tool", Image: "tool:latest", PullPolicy: "always",
	}}}
	fd := &fakeDocker{}
	cmd := project.CommandContext(ctx, "tool")
	cmd.docker = fd
	if err := cmd.Start(); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v want cancellation", err)
	}
	if fd.imagePullCalls != 0 || fd.createCalls != 0 {
		t.Fatal("canceled command pulled or created a container")
	}
}

func TestRepeatedCommandsInspectCurrentMetadata(t *testing.T) {
	project := &Project{Services: types.Services{"tool": {
		Name: "tool", Image: "tool:latest", PullPolicy: "daily",
	}}}
	// Stop at container creation to avoid unrelated attach/lifecycle setup.
	createErr := errors.New("creation reached")
	fd := &fakeDocker{createErr: createErr}
	steps := []struct {
		age       time.Duration
		wantPulls int
	}{
		{age: 0, wantPulls: 0},
		{age: 48 * time.Hour, wantPulls: 1},
		{age: 0, wantPulls: 1},
	}
	for i, step := range steps {
		fd.imageInspectResp.Metadata.LastTagTime = time.Now().Add(-step.age)
		cmd := project.Command("tool")
		cmd.docker = fd
		if err := cmd.Start(); !errors.Is(err, createErr) {
			t.Fatalf("execution %d: %v", i, err)
		}
		if fd.imagePullCalls != step.wantPulls {
			t.Fatalf("execution %d: pull calls=%d want=%d", i, fd.imagePullCalls, step.wantPulls)
		}
	}
	if fd.imageInspectCalls != 3 || fd.createCalls != 3 {
		t.Fatalf("inspect=%d create=%d", fd.imageInspectCalls, fd.createCalls)
	}
}
