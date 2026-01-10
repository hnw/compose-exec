package compose

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func maybeWarnMissingComposeFileInContainer(wd string) {
	if wd == "" {
		return
	}
	if !isProbablyRunningInContainer() {
		return
	}
	if hasComposeFile(wd) {
		return
	}
	writeWarning(
		os.Stderr,
		"Running inside a container but 'docker-compose.yml' is not found. Ensure the host's current directory is mounted to the same path inside this container (Mirror Mount).",
	)
}

func hasComposeFile(dir string) bool {
	if dir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.yaml")); err == nil {
		return true
	}
	return false
}

func isProbablyRunningInContainer() bool {
	// Most Docker containers have this marker.
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}

	// Fallback heuristic.
	b, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(b)
	// Common markers across Docker / containerd / Kubernetes.
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd") ||
		strings.Contains(s, "kubepods")
}

func writeWarning(w io.Writer, msg string) {
	if w == nil {
		return
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	_, _ = fmt.Fprintf(w, "[compose-exec] Warning: %s\n", msg)
}
