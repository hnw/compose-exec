# compose-exec

[![Go Reference](https://pkg.go.dev/badge/github.com/hnw/compose-exec.svg)](https://pkg.go.dev/github.com/hnw/compose-exec)
[Japanese README (日本語ドキュメント)](./README_ja.md)

**Native Docker Compose Execution for Go: Zero-Dependency Integration Testing & Minimalist DooD.**

`compose-exec` is a Go library that manages Docker Compose services directly via the Docker Engine API, using your existing `docker-compose.yml` as the **Single Source of Truth**.

It eliminates dependencies on the `docker` binary or external shell scripts, enabling truly portable integration tests and ultra-lightweight container orchestration agents.

```mermaid
graph LR
    classDef host fill:#fafafa,stroke:#666,stroke-width:2px,color:#333;
    classDef container fill:#e3f2fd,stroke:#1565c0,stroke-width:2px,color:#0d47a1;
    classDef daemon fill:#1565c0,stroke:#fff,stroke-width:0px,color:#fff;
    classDef target fill:#fff3e0,stroke:#ef6c00,stroke-dasharray: 5 5,color:#e65100;

    subgraph Host ["Host Machine"]
        File["docker-compose.yml"]:::host
        Daemon[["Docker Daemon"]]:::daemon
    end

    subgraph Controller ["Go Process<br>(Host or Container)"]
        Lib["compose-exec"]:::container
    end

    Target("Target Container"):::target

    Lib -- "1. Read Config" --> File
    Lib -- "2. Call API (Socket)" --> Daemon
    Daemon -- "3. Spawn (DooD)" --> Target

    class Host host;
    class Controller container;

```

## ⚡ Core Values

### 1. Single Source of Truth

Stop redefining your container specs in Go structs. `compose-exec` reads your existing `docker-compose.yml` directly. This prevents configuration drift and ensures you test against the exact same definition used in production/development.

### 2. Orchestration as Code

Handle complex lifecycles with robust Go logic. Use helpers like `WaitUntilHealthy` to ensure databases or services are fully ready before running tests, stabilizing flaky integration suites.

### 3. Minimalist DooD (Docker out of Docker)

No `docker` CLI. No shell.
Because it communicates directly with the Docker API, your app can run on `distroless` or `scratch` images. This drastically reduces image size for CI runners or bots and **minimizes the attack surface** by removing dangerous binaries from the runtime environment.

---

## 🚀 Quick Start

### Installation

```bash
go get [github.com/hnw/compose-exec](https://github.com/hnw/compose-exec)

```

> **Note:** The library handles image pulling and container creation automatically based on your Compose file.

### Example: Waiting for Health & Execution

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
	// Loads config for the "sibling" service.
	svc := compose.From("sibling")

	// 2. Define the command
	cmd := svc.Command("echo", "Hello from container")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// 3. (Optional) Wait for Healthcheck
	// If a healthcheck is defined in YAML, this blocks until the container is healthy.
	// Perfect for waiting for DBs or API services.
	// if err := cmd.WaitUntilHealthy(ctx); err != nil {
	// 	panic(err)
	// }

	// 4. Run and Cleanup
	// Manages the full lifecycle: Start -> Attach -> Stop -> Remove
	if err := cmd.Run(ctx); err != nil {
		panic(err)
	}
}

```

---

## 🛠 Use Cases

### A. Self-Contained Integration Testing

Run your entire test suite—infrastructure setup, execution, and teardown—with a single `go test` command. No `Makefile` or external scripts required.

### B. Lightweight Agents / ChatOps

Ideal for building bots (e.g., Slack/Discord) that execute specific containerized tasks.
Since no `docker` binary is present in the container, it mitigates the risk of attackers leveraging standard tools for privilege escalation in case of a compromise.

---

## ⚙️ DooD Configuration (CI / Container)

To use `compose-exec` inside a container, you must configure a **Mirror Mount** to ensure the host file paths resolve correctly.

```yaml
services:
  # 1. Controller (Your Go App / CI Runner)
  controller:
    image: gcr.io/distroless/static-debian12:latest
    volumes:
      # Required: Access to Docker API
      - /var/run/docker.sock:/var/run/docker.sock
      # Required: Mirror Mount
      # Map host's current directory to the exact same path inside the container
      - .:${PWD}
    working_dir: ${PWD}
    # Optional: Avoid permission issues with host files
    user: "${UID}:${GID}"

  # 2. Target Sibling (The service being called)
  sibling:
    image: alpine:latest
    profiles:
      - manual # Prevent auto-start

```

## Requirements

* **Go:** 1.22+
* **Docker Engine:** API version 1.40+
* **OS:** Linux, macOS (Docker Desktop), Windows (WSL2 recommended)

## License

MIT
