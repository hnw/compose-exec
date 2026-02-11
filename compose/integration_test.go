//go:build integration

package compose

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
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

	svc, err = proj.Service("alpine")
	if err != nil {
		t.Fatalf("Project.Service: %v", err)
	}

	// Ensure per-test Compose resources (networks, etc.) are removed.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := Down(ctx, proj.Name); err != nil {
			t.Logf("Down: %v", err)
		}
	})
	return dir, svc
}

func setupIntegrationWithComposeYAML(t *testing.T, yaml string) (dir string, proj *Project) {
	t.Helper()
	requireDocker(t)

	// Keep a Go-managed temp dir for isolation bookkeeping.
	_ = t.TempDir()

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

	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write compose yaml: %v", err)
	}

	proj, err = LoadProject(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}

	// Ensure per-test Compose resources (networks, etc.) are removed.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := Down(ctx, proj.Name); err != nil {
			t.Logf("Down: %v", err)
		}
	})
	return dir, proj
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := svc.CommandContext(ctx, "sh", "-c", "echo hello world")
	out, err := cmd.Output()
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := svc.CommandContext(ctx, "cat", "/data/host_token.txt")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != token {
		t.Fatalf("stdout=%q want=%q", got, token)
	}
}

func TestIntegration_NamedVolumePersistence(t *testing.T) {
	yaml := "" +
		"volumes:\n" +
		"  db_data:\n" +
		"services:\n" +
		"  alpine:\n" +
		"    image: alpine:latest\n" +
		"    command: top\n" +
		"    volumes:\n" +
		"      - db_data:/data\n"

	_, proj := setupIntegrationWithComposeYAML(t, yaml)

	svc, err := proj.Service("alpine")
	if err != nil {
		t.Fatalf("Project.Service: %v", err)
	}

	// Cleanup the created named volume (Down() intentionally does not remove volumes).
	volName := fmt.Sprintf("%s_%s", proj.Name, "db_data")
	t.Cleanup(func() {
		cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			return
		}
		defer cli.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = cli.VolumeRemove(ctx, volName, true)
	})

	token := randToken(t)

	ctx1, cancel1 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel1()
	cmd1 := svc.CommandContext(ctx1, "sh", "-c", fmt.Sprintf("echo %s > /data/token.txt", token))
	if err := cmd1.Run(); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()
	cmd2 := svc.CommandContext(ctx2, "cat", "/data/token.txt")
	out, err := cmd2.Output()
	if err != nil {
		t.Fatalf("second Output: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != token {
		t.Fatalf("stdout=%q want=%q", got, token)
	}
}

func TestIntegration_EnvironmentVariableInjection(t *testing.T) {
	_, svc := setupIntegration(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := svc.CommandContext(ctx, "sh", "-c", "echo $TEST_VAR")
	cmd.Env = []string{"TEST_VAR=integration_success"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(string(out), "integration_success") {
		t.Fatalf("stdout=%q", string(out))
	}
}

func TestIntegration_ExitCodePropagation(t *testing.T) {
	_, svc := setupIntegration(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := svc.CommandContext(ctx, "sh", "-c", "exit 42")
	err := cmd.Run()
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

func TestIntegration_OOMKilled(t *testing.T) {
	yaml := `
services:
  oom:
    image: python:3.12-alpine
    mem_limit: 32m
    memswap_limit: 32m
    command: ["python3", "-c", "l=[]; [l.append(' ' * 1024 * 1024) for _ in range(1024)]"]
`
	_, proj := setupIntegrationWithComposeYAML(t, yaml)
	svc, err := proj.Service("oom")
	if err != nil {
		t.Fatalf("Project.Service: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := svc.CommandContext(ctx)
	err = cmd.Run()
	if err == nil {
		t.Fatalf("expected error")
	}
	var ee *ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *ExitError, got %T: %v", err, err)
	}
	if ee.ContainerState == nil {
		t.Fatalf("ContainerState is nil")
	}
	if !ee.ContainerState.OOMKilled {
		t.Fatalf("OOMKilled=false exit=%d err=%v", ee.ExitCode(), err)
	}
}

func TestIntegration_SignalPropagationZombiePrevention(t *testing.T) {
	_, svc := setupIntegration(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := svc.CommandContext(ctx, "sleep", "10")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	time.Sleep(1 * time.Second)
	cancelAt := time.Now()
	cancel()

	err := cmd.Wait()
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
			cmd := svc.CommandContext(ctx, "sh", "-c", fmt.Sprintf("echo -n ok-%d", i))
			cmd.docker = cli
			out, err := cmd.Output()
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := svc.CommandContext(ctx, "sh", "-c", "echo -n ${#BIG_A},${#BIG_B},${#BIG_C}")
	cmd.Env = []string{
		"BIG_A=" + valA,
		"BIG_B=" + valB,
		"BIG_C=" + valC,
	}
	out, err := cmd.Output()
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := svc.CommandContext(ctx, "this-command-should-not-exist")
	err := cmd.Run()
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
	// Reproduce the example: Controller runs locally, then Target runs via compose.Command("target").
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

	siblingCmd := CommandContext(ctx, "target", "cat", "/etc/os-release")
	out, err := siblingCmd.Output()
	if err != nil {
		t.Fatalf("sibling container ('target') Run: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(out)), "alpine") {
		t.Fatalf("stdout=%q (expected alpine)", string(out))
	}
}

func TestIntegration_WaitUntilHealthy(t *testing.T) {
	yaml := "" +
		"services:\n" +
		"  hc:\n" +
		"    image: alpine:latest\n" +
		"    command: sleep 60\n" +
		"    volumes:\n" +
		"      - .:/data\n" +
		"    healthcheck:\n" +
		"      test: [\"CMD-SHELL\", \"test -f /data/healthy\"]\n" +
		"      interval: 1s\n" +
		"      timeout: 1s\n" +
		"      retries: 30\n"

	dir, proj := setupIntegrationWithComposeYAML(t, yaml)
	svc, err := proj.Service("hc")
	if err != nil {
		t.Fatalf("Project.Service: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Keep the container alive until the healthcheck flips to healthy.
	cmd := svc.CommandContext(ctx, "sh", "-c", "while [ ! -f /data/healthy ]; do sleep 0.1; done; sleep 1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	go func() {
		// Ensure at least one healthcheck cycle happens while we are waiting.
		time.Sleep(1500 * time.Millisecond)
		_ = os.WriteFile(filepath.Join(dir, "healthy"), []byte("ok"), 0o644)
	}()

	if err := cmd.WaitUntilHealthy(); err != nil {
		t.Fatalf("WaitUntilHealthy: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestIntegration_PrivilegedAndCapabilitiesMapping(t *testing.T) {
	// Privileged: verify a privileged-only operation succeeds.
	// CapAdd/CapDrop: verify they are forwarded into HostConfig via inspect.
	yaml := "" +
		"services:\n" +
		"  priv:\n" +
		"    image: alpine:latest\n" +
		"    privileged: true\n" +
		"  unpriv:\n" +
		"    image: alpine:latest\n" +
		"  caps:\n" +
		"    image: alpine:latest\n" +
		"    cap_add: [\"NET_ADMIN\"]\n" +
		"    cap_drop: [\"MKNOD\"]\n"

	_, proj := setupIntegrationWithComposeYAML(t, yaml)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	mountCmd := "mkdir -p /mnt && mount -t tmpfs tmpfs /mnt && umount /mnt"

	privSvc, err := proj.Service("priv")
	if err != nil {
		t.Fatalf("Project.Service(priv): %v", err)
	}
	if err := privSvc.CommandContext(ctx, "sh", "-c", mountCmd).Run(); err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "operation not permitted") || strings.Contains(msg, "permission denied") {
			t.Skipf("privileged operation unsupported in this environment: %v", err)
		}
		t.Fatalf("privileged run: %v", err)
	}

	unprivSvc, err := proj.Service("unpriv")
	if err != nil {
		t.Fatalf("Project.Service(unpriv): %v", err)
	}
	if err := unprivSvc.CommandContext(ctx, "sh", "-c", mountCmd).Run(); err == nil {
		t.Fatalf("expected unprivileged mount to fail")
	}

	capsSvc, err := proj.Service("caps")
	if err != nil {
		t.Fatalf("Project.Service(caps): %v", err)
	}

	capsCmd := capsSvc.CommandContext(ctx, "sleep", "2")
	if err := capsCmd.Start(); err != nil {
		t.Fatalf("caps Start: %v", err)
	}
	st, err := capsCmd.snapshotWaitState()
	if err != nil {
		t.Fatalf("caps snapshot: %v", err)
	}
	j, err := st.dc.ContainerInspect(ctx, st.id)
	if err != nil {
		t.Fatalf("caps inspect: %v", err)
	}
	if j.HostConfig == nil {
		t.Fatalf("caps inspect: HostConfig is nil")
	}
	capAdd := []string(j.HostConfig.CapAdd)
	capDrop := []string(j.HostConfig.CapDrop)
	if !containsCapability(capAdd, "NET_ADMIN") {
		t.Fatalf("CapAdd=%v (expected NET_ADMIN)", capAdd)
	}
	if !containsCapability(capDrop, "MKNOD") {
		t.Fatalf("CapDrop=%v (expected MKNOD)", capDrop)
	}
	if err := capsCmd.Wait(); err != nil {
		t.Fatalf("caps Wait: %v", err)
	}
}

func TestIntegration_DownRemovesContainers(t *testing.T) {
	yaml := "" +
		"services:\n" +
		"  alpine:\n" +
		"    image: alpine:latest\n" +
		"    command: sleep 60\n"

	_, proj := setupIntegrationWithComposeYAML(t, yaml)

	svc, err := proj.Service("alpine")
	if err != nil {
		t.Fatalf("Project.Service: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := svc.CommandContext(ctx)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	st, err := cmd.snapshotWaitState()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	containerID := st.id

	downCtx, cancelDown := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelDown()
	if err := Down(downCtx, proj.Name); err != nil {
		t.Fatalf("Down: %v", err)
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	defer cli.Close()

	inspectCtx, cancelInspect := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelInspect()
	_, err = cli.ContainerInspect(inspectCtx, containerID)
	if err == nil {
		t.Fatalf("expected container to be removed")
	}
	if !cerrdefs.IsNotFound(err) && !strings.Contains(strings.ToLower(err.Error()), "not found") {
		t.Fatalf("unexpected inspect error: %v", err)
	}

	_ = cmd.Wait()
}

func containsCapability(ss []string, want string) bool {
	want = strings.ToUpper(want)
	alt := "CAP_" + want
	for _, s := range ss {
		s = strings.ToUpper(s)
		if s == want || s == alt {
			return true
		}
	}
	return false
}

func TestCommand_EdgeCases(t *testing.T) {
	t.Run("CaseA_DefaultCommand_UsesYAMLCommand", func(t *testing.T) {
		yaml := "" +
			"services:\n" +
			"  s:\n" +
			"    image: alpine:latest\n" +
			"    command: [\"sh\", \"-c\", \"echo -n from-yaml\"]\n"

		_, proj := setupIntegrationWithComposeYAML(t, yaml)
		svc, err := proj.Service("s")
		if err != nil {
			t.Fatalf("Project.Service: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := svc.CommandContext(ctx)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("Output: %v", err)
		}
		if got := strings.TrimSpace(string(out)); got != "from-yaml" {
			t.Fatalf("stdout=%q want=%q", got, "from-yaml")
		}
	})

	t.Run("CaseA_DefaultCommand_UsesImageDefaultWhenNoYAMLCommand", func(t *testing.T) {
		// hello-world prints and exits using image defaults (no service.command).
		yaml := "" +
			"services:\n" +
			"  s:\n" +
			"    image: hello-world:latest\n"

		_, proj := setupIntegrationWithComposeYAML(t, yaml)
		svc, err := proj.Service("s")
		if err != nil {
			t.Fatalf("Project.Service: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := svc.CommandContext(ctx)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("Output: %v", err)
		}
		if !strings.Contains(string(out), "Hello from Docker!") {
			t.Fatalf("stdout=%q", string(out))
		}
	})

	t.Run("CaseB_NormalArgs_EchoHello", func(t *testing.T) {
		_, svc := setupIntegration(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := svc.CommandContext(ctx, "echo", "hello")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("Output: %v", err)
		}
		if got := strings.TrimSpace(string(out)); got != "hello" {
			t.Fatalf("stdout=%q want=%q", got, "hello")
		}
	})

	t.Run("CaseC_EmptyStringCommand_Errors", func(t *testing.T) {
		_, svc := setupIntegration(t)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := svc.CommandContext(ctx, "").Run(); err == nil {
			t.Fatalf("expected error")
		}
		if err := svc.CommandContext(ctx, "", "arg").Run(); err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("CaseD_Priority_ExplicitOverridesYAMLCommand", func(t *testing.T) {
		yaml := "" +
			"services:\n" +
			"  s:\n" +
			"    image: alpine:latest\n" +
			"    command: [\"sleep\", \"10\"]\n"

		_, proj := setupIntegrationWithComposeYAML(t, yaml)
		svc, err := proj.Service("s")
		if err != nil {
			t.Fatalf("Project.Service: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := svc.CommandContext(ctx, "echo", "override")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("Output: %v", err)
		}
		if got := strings.TrimSpace(string(out)); got != "override" {
			t.Fatalf("stdout=%q want=%q", got, "override")
		}
	})
}

func TestIntegration_StdoutPipe_FastExit(t *testing.T) {
	_, svc := setupIntegration(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := svc.CommandContext(ctx, "/bin/echo", "-n", "pipe-data")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	out, err := io.ReadAll(stdout)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if string(out) != "pipe-data" {
		t.Errorf("StdoutPipe mismatch. got=%q, want=%q", string(out), "pipe-data")
	}
}

type faultyWriter struct {
	err error
}

func (fw *faultyWriter) Write(p []byte) (n int, err error) {
	return 0, fw.err
}

func TestIntegration_WriterError_SilentFailure(t *testing.T) {
	_, svc := setupIntegration(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := svc.CommandContext(ctx, "/bin/echo", "should-fail")

	expectedErr := errors.New("simulated writer error")
	cmd.Stdout = &faultyWriter{err: expectedErr}

	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	err := cmd.Wait()

	if err == nil {
		t.Fatalf("expected writer error, got nil")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected writer error %v, got %v", expectedErr, err)
	}
}
