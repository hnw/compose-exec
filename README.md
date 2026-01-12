# compose-exec

[![Go Reference](https://pkg.go.dev/badge/github.com/hnw/compose-exec.svg)](https://pkg.go.dev/github.com/hnw/compose-exec)
[Japanese README (日本語ドキュメント)](./README_ja.md)

**Native Docker Compose Automation for Go: No CLI, No Shell, Just Code.**

`compose-exec` is a Go library that manages Docker Compose services directly via the Docker Engine API, treating your existing `docker-compose.yml` as the definition.
It eliminates the need for the `docker` binary and shell scripts, providing a safer, more robust alternative to `os/exec`.

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

### 1. Robust Container Lifecycle
Using `exec.Command` to drive the Docker CLI often leads to zombie containers if the Go process is interrupted.
`compose-exec` communicates directly with the API, ensuring that container lifecycle is strictly tied to your Go `Context`. When your test times out or is cancelled, the containers are guaranteed to stop.

### 2. Zero Docker CLI Dependency
No need to install the `docker` binary in your runtime environment.
This means your tests and tools can run inside `distroless` or `scratch` images, keeping your CI runners lightweight and fast. It also ensures consistent behavior across Windows, Mac, and Linux, regardless of the host shell.

### 3. Injection-Proof Architecture
Constructing shell commands with user input always carries the risk of OS command injection.
By bypassing the shell and using the Docker Socket directly, this library **structurally eliminates the possibility of command injection**. This allows you to build secure ChatOps bots and internal tools with confidence.

## 🏃 See it in action

This repository itself serves as a functional DooD demo.
Run the following to see the Sibling Container Pattern in action, simulating a real CI environment.

```bash
# 1. Clone
git clone [https://github.com/hnw/compose-exec.git](https://github.com/hnw/compose-exec.git)
cd compose-exec

# 2. Run the demo
# This starts the "app" container, which then dynamically orchestrates the "sibling" container.
docker compose run app

```

---

## 🚀 Quick Start

### Installation

```bash
go get github.com/hnw/compose-exec

```

> **Note:** The library handles image pulling and container creation automatically based on your Compose file.

### Example: Waiting for Health & Execution

```go
package main

import (
	"context"
	"os"
	"github.com/hnw/compose-exec/compose"
)

func main() {
	ctx := context.Background()

	// 1. Target a service defined in docker-compose.yml
	// Loads config for the "target" service.
	svc := compose.From("target")

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
  target:
    image: alpine:latest
    profiles:
      - manual # Prevent auto-start

```

## Troubleshooting

* **permission denied (docker.sock):**
If using Rootless Docker, Lima, or Colima, the container may not have write access to the socket.
  * Ensure `user: "${UID}:${GID}"` is set
  * or check the socket permissions on the host

* **file not found (mounts):**
In a DooD environment, bind mount paths are interpreted as host paths. If the path inside the container does not match the host path, the mount will fail. Please verify the "Mirror Mount" configuration described above.

## Requirements

* **Go:** 1.24+
* **Docker Engine:** API version 1.40+
* **OS:** Linux, macOS (Docker Desktop), Windows (WSL2 recommended)

## License

MIT
