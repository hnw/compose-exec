# compose-exec

[![Go Reference](https://pkg.go.dev/badge/github.com/hnw/compose-exec.svg)](https://pkg.go.dev/github.com/hnw/compose-exec)

[Japanese README (日本語ドキュメント)](./README_ja.md)

**The Missing Link for Secure, Portable DevOps in Go.**

`compose-exec` is a dependency-free Go library for executing commands inside Docker Compose services. It provides an interface identical to `os/exec` and implements the **Sibling Container Pattern** in the most secure and robust way possible.

## ⚡ Why compose-exec?

The traditional approach of installing the Docker CLI inside a container is broken in terms of security and portability. This library is the logical answer to modern DevOps challenges.

### 1. Zero-Trust & Distroless Ready

Minimizes the attack surface to the absolute limit.
* **No Docker CLI Required:** No need to install `docker` binaries or Python-based Compose inside your container.
* **No Shell Required:** Does not require `/bin/sh`.
* This enables usage with **[Distroless](https://github.com/GoogleContainerTools/distroless)** (static) or `scratch` images. Orchestrate containers from an environment with zero tools available to an attacker.

### 2. Pipeline as Code (No More YAML Hell)

Liberate your CI/CD logic from vendor-specific YAML (GitHub Actions, GitLab CI).
* **Portable Logic:** Define build, test, and deploy steps as Go code.
* **Reproducibility:** Eliminates "Works on my machine, fails on CI". As long as you have Go and Docker, your pipeline runs exactly the same way on a MacBook as it does on a CI Runner.

### 3. Native Go Experience

* **Zero Learning Curve:** The API mirrors `os/exec`.
* **Robustness:** Automatically handles signal forwarding (SIGINT/SIGTERM), zombie process prevention (PID 1 issues), and exit code propagation.

---

## 🚀 Quick Start

### Installation

```bash
go get [github.com/hnw/compose-exec](https://github.com/hnw/compose-exec)
```

### Usage

The code feels exactly like `os/exec`.

```go
package main

import (
	"context"
	"os"
	"[github.com/hnw/compose-exec/compose](https://github.com/hnw/compose-exec/compose)"
)

func main() {
	ctx := context.Background()

	// 1. Target a service defined in docker-compose.yml
	// Image pulling, container creation, and networking are handled automatically.
	cmd := compose.From("sibling-target").Command("ls", "-la", "/app")

	// 2. Connect pipes
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// 3. Run
	// The container is automatically cleaned up after execution.
	if err := cmd.Run(ctx); err != nil {
		// Exit codes are correctly propagated
		panic(err)
	}
}
```

---

## 💡 Real-World Use Cases

The true value lies not just in running commands, but in "Infrastructure as Code" for tasks.

### Scenario A: Self-Contained Integration Tests

Your test code manages the lifecycle of its required infrastructure (DB, Cache, etc.). No need to define `services` in your CI YAML.

```go
func TestWithDatabase(t *testing.T) {
    // Launch DB on demand within the test
    // Portable across any CI environment
    dbCmd := compose.From("postgres").Command("docker-entrypoint.sh", "postgres")
    dbCmd.Start(context.Background())
    defer dbCmd.Wait() // Teardown on test completion

    // ... run tests ...
}
```

### Scenario B: Portable CI/CD Runner

Write complex build flows (Frontend build -> Static Asset -> Go Embed) in Go and run them as a custom runner.

* No need to install Node.js/npm on the developer's machine.
* `compose-exec` spins up a Node.js container, builds the assets, and exits.

---

## ⚙️ Configuration

To use `compose-exec` from within a container (Sibling Container Pattern), your `docker-compose.yml` requires specific setup.

### 1. Mirror Mount (`.:${PWD}`)

Docker Bind Mounts always reference host OS paths. To resolve relative paths correctly, you must mount the host's current directory to the **exact same path** inside the container.

### 2. Manual Profile

Use profiles to prevent the target (Sibling) container from starting automatically with `docker compose up`.

### Recommended `docker-compose.yml`

```yaml
services:
  # 1. Controller (Your Go App / CI Runner)
  #    Can be a Distroless image
  controller:
    image: gcr.io/distroless/static-debian12:latest
    volumes:
      # Required: Access to Docker API
      - /var/run/docker.sock:/var/run/docker.sock
      # Required: Mirror Mount
      # Maps host's current dir to the SAME path inside the container
      - .:${PWD}
    working_dir: ${PWD}
    # Optional: Inject host UID/GID to avoid permission issues
    user: "${UID}:${GID}"
    command: ["/path/to/your-go-binary"]

  # 2. Target Sibling
  sibling-target:
    image: alpine:latest
    profiles:
      - manual # Only starts when called by compose-exec
```

---

## ⚠️ Requirements & Compatibility

* **Go:** 1.22+
* **Docker Engine:** API version 1.40+
* **OS:** Linux, macOS (Docker Desktop), Windows (WSL2 recommended)

### Note on Image Size

This library includes the Docker SDK, which increases binary size. However, because it removes the need to bundle the `docker` CLI binary (~50MB) and a base OS shell, **the total container image size is significantly reduced.**

---

## License

MIT
