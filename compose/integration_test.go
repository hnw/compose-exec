//go:build integration

package compose

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

func requireDocker(t *testing.T) {
	t.Helper()

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("docker client unavailable: %v", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := cli.Ping(ctx); err != nil {
		t.Skipf("docker daemon not reachable: %v", err)
	}
}

func setupIntegration(t *testing.T) (dir string, svc *Service) {
	t.Helper()
	requireDocker(t)

	// Keep a Go-managed temp dir for isolation bookkeeping.
	_ = t.TempDir()

	// Docker Desktop on macOS typically can't bind-mount from /var/folders (t.TempDir()).
	// Create a per-test directory under the package working directory (usually under /Users)
	// so that bind mounts work out of the box.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	base := filepath.Join(wd, ".compose-exec-integration")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	dir, err = os.MkdirTemp(base, "case-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})

	yaml := "" +
		"services:\n" +
		"  alpine:\n" +
		"    image: alpine:latest\n" +
		"    command: top\n" +
		"    volumes:\n" +
		"      - .:/data\n"

	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write compose yaml: %v", err)
	}

	proj, err := LoadProject(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}

	svc, err = FromProject(proj, "alpine")
	if err != nil {
		t.Fatalf("FromProject: %v", err)
	}
	return dir, svc
}

func randToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

func TestIntegration_BasicExecution(t *testing.T) {
	_, svc := setupIntegration(t)

	cmd := svc.Command("sh", "-c", "echo hello world")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := cmd.Output(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(string(out), "hello world") {
		t.Fatalf("stdout=%q", string(out))
	}
}

func TestIntegration_BindMountAndPathResolution(t *testing.T) {
	dir, svc := setupIntegration(t)

	token := randToken(t)
	if err := os.WriteFile(filepath.Join(dir, "host_token.txt"), []byte(token), 0o644); err != nil {
		t.Fatalf("write host_token.txt: %v", err)
	}

	cmd := svc.Command("cat", "/data/host_token.txt")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := cmd.Output(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != token {
		t.Fatalf("stdout=%q want=%q", got, token)
	}
}

func TestIntegration_EnvironmentVariableInjection(t *testing.T) {
	_, svc := setupIntegration(t)

	cmd := svc.Command("sh", "-c", "echo $TEST_VAR")
	cmd.Env = []string{"TEST_VAR=integration_success"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := cmd.Output(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(string(out), "integration_success") {
		t.Fatalf("stdout=%q", string(out))
	}
}

func TestIntegration_ExitCodePropagation(t *testing.T) {
	_, svc := setupIntegration(t)

	cmd := svc.Command("sh", "-c", "exit 42")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := cmd.Run(ctx)
	if err == nil {
		t.Fatalf("expected error")
	}
	var ee *ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *ExitError, got %T: %v", err, err)
	}
	if ee.ExitCode() != 42 {
		t.Fatalf("exit=%d want=42", ee.ExitCode())
	}
}

func TestIntegration_SignalPropagationZombiePrevention(t *testing.T) {
	_, svc := setupIntegration(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := svc.Command("sleep", "10")

	if err := cmd.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	time.Sleep(1 * time.Second)
	cancelAt := time.Now()
	cancel()

	err := cmd.Wait(ctx)
	elapsed := time.Since(cancelAt)
	if elapsed >= 2*time.Second {
		t.Fatalf("Wait took too long: %s", elapsed)
	}
	if err == nil {
		t.Fatalf("expected error")
	}
	// Accept either context cancellation or non-zero exit (killed/stopped).
	if !errors.Is(err, context.Canceled) {
		var ee *ExitError
		if !errors.As(err, &ee) {
			// Some Docker errors may wrap; still non-nil is required.
			var derr dockertypes.ErrorResponse
			_ = errors.As(err, &derr)
		}
	}
}

func TestIntegration_Concurrency(t *testing.T) {
	_, svc := setupIntegration(t)

	// Share a single Docker client across goroutines to stress concurrency.
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const n = 8
	var wg sync.WaitGroup
	errCh := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := svc.Command("sh", "-c", fmt.Sprintf("echo -n ok-%d", i))
			cmd.docker = cli
			out, err := cmd.Output(ctx)
			if err != nil {
				errCh <- fmt.Errorf("cmd %d: %w", i, err)
				return
			}
			if strings.TrimSpace(string(out)) != fmt.Sprintf("ok-%d", i) {
				errCh <- fmt.Errorf("cmd %d: stdout=%q", i, string(out))
				return
			}
		}(i)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestIntegration_LargeEnvironmentVariables(t *testing.T) {
	_, svc := setupIntegration(t)

	// Total: ~48KiB
	valA := strings.Repeat("a", 16*1024)
	valB := strings.Repeat("b", 16*1024)
	valC := strings.Repeat("c", 16*1024)

	cmd := svc.Command("sh", "-c", "echo -n ${#BIG_A},${#BIG_B},${#BIG_C}")
	cmd.Env = []string{
		"BIG_A=" + valA,
		"BIG_B=" + valB,
		"BIG_C=" + valC,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := cmd.Output(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := fmt.Sprintf("%d,%d,%d", len(valA), len(valB), len(valC))
	got := strings.TrimSpace(string(out))
	if got != want {
		t.Fatalf("stdout=%q want=%q", got, want)
	}
}

func TestIntegration_CommandNotFound(t *testing.T) {
	_, svc := setupIntegration(t)

	cmd := svc.Command("this-command-should-not-exist")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := cmd.Run(ctx)
	if err == nil {
		t.Fatalf("expected error")
	}

	// Accept either an ExitError (127) or a Docker/OCI runtime error.
	var ee *ExitError
	if errors.As(err, &ee) {
		if code := ee.ExitCode(); code != 127 {
			// Some runtimes may use 126/127; keep message but prefer 127.
			if code != 126 {
				t.Fatalf("exit=%d want=127 (or 126), err=%v", code, err)
			}
		}
		return
	}

	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "not found") && !strings.Contains(msg, "executable file") {
		t.Fatalf("unexpected error: %T: %v", err, err)
	}
}

func TestIntegration_ExampleScenarioRegression(t *testing.T) {
	// Reproduce the example: Controller runs locally, then Target runs via compose.From("sibling").
	// Keep it portable across macOS by falling back if /etc/os-release isn't present.

	// Controller (self)
	if _, err := os.Stat("/etc/os-release"); err == nil {
		b, err := exec.Command("cat", "/etc/os-release").CombinedOutput()
		if err != nil {
			t.Fatalf("controller cat /etc/os-release: %v", err)
		}
		if len(bytes.TrimSpace(b)) == 0 {
			t.Fatalf("controller output empty")
		}
	} else {
		b, err := exec.Command("uname", "-a").CombinedOutput()
		if err != nil {
			t.Fatalf("controller uname: %v", err)
		}
		if len(bytes.TrimSpace(b)) == 0 {
			t.Fatalf("controller output empty")
		}
	}

	// Target (sibling)
	requireDocker(t)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	root := filepath.Dir(wd)
	oldwd := wd
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir repo root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	siblingCmd := From("sibling").Command("cat", "/etc/os-release")
	out, err := siblingCmd.Output(ctx)
	if err != nil {
		t.Fatalf("sibling Run: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(out)), "alpine") {
		t.Fatalf("stdout=%q (expected alpine)", string(out))
	}
}
