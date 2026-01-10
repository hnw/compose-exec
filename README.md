# compose-exec

[![Go Reference](https://pkg.go.dev/badge/github.com/hnw/compose-exec.svg)](https://pkg.go.dev/github.com/hnw/compose-exec)
[Japanese README (日本語ドキュメント)](./README_ja.md)

**Programmatic Docker Compose for Go: Self-Contained Integration Testing & Lightweight, Secure DooD.**

`compose-exec` is a Go library to manage Docker Compose services directly via the Docker Engine API.

It allows you to reuse your existing `docker-compose.yml` and control container lifecycles using the familiar `os/exec` interface. By eliminating dependencies on external shell scripts or the `docker` binary, it enables truly portable and "Self-Contained" integration testing workflows.

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

## ⚡ Core Value: Why compose-exec?

### 1. Self-Contained Integration Testing

Break free from "shell script dependency hell."
Directly manage the lifecycle of infrastructure (databases, servers) using only Go code, without relying on the `docker` CLI or shell environments.

* **Go Native Control:** Handle complex logic like error handling, timeouts, and signal forwarding with robust Go code instead of fragile shell scripts.
* **Bring Your Own YAML:** No need to learn a new configuration language. Reuse the exact same `docker-compose.yml` you use in production.

### 2. Ultra-Lightweight & Efficient

Eliminate the overhead of the heavy `docker` CLI binary and shell environments.
Since it communicates directly with the Docker Engine API, it runs as a single binary, saving resources on CI runners and edge devices, and accelerating your pipelines.

* **Minimal Footprint:** Runs on `Distroless` or `scratch` images, reducing image size to just a few MBs. Perfect for resource-constrained Edge/IoT environments.
* **Secure by Design:** As a side effect, removing the shell and external tools minimizes the attack surface, enabling a naturally secure DooD environment.

---

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

## 🚀 Getting Started

### Installation

```bash
go get [github.com/hnw/compose-exec](https://github.com/hnw/compose-exec)

```

> **Note:** `compose-exec` strictly focuses on executing existing images. It does not perform `docker compose build`. If an image is missing, it will be pulled automatically. Please build images beforehand if source code changes are needed.

### Quick Start

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
	// The library automatically handles image pulling, networking, and cleanup.
	cmd := compose.From("sibling-target").Command("ls", "-la", "/app")

	// 2. Connect pipes
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// 3. Run
	if err := cmd.Run(ctx); err != nil {
		// Exit codes are propagated correctly
		panic(err)
	}
}

```

---

## ⚙️ CI / Container Setup (DooD Configuration)

To use `compose-exec` inside a container (DooD pattern), your `docker-compose.yml` requires the following configuration.

### Required: Mirror Mount

To correctly locate your `docker-compose.yml` and resolve file paths, you must mount the host's current directory to the exact same path inside the container.

```yaml
services:
  # 1. Controller (Your Go App / CI Runner)
  controller:
    image: gcr.io/distroless/static-debian12:latest
    volumes:
      # Required: Access to Docker API
      - /var/run/docker.sock:/var/run/docker.sock
      # Required: Mirror Mount
      # Map host's current directory to the same location inside container
      - .:${PWD}
    working_dir: ${PWD}
    # Optional: Fix permission issues on host files
    user: "${UID}:${GID}"

  # 2. Target Sibling (The service being called)
  sibling-target:
    image: alpine:latest
    profiles:
      - manual # Prevent auto-start

```

## Troubleshooting

* **permission denied (docker.sock):**
If using Rootless Docker, Lima, or Colima, the container may not have write access to the socket. Ensure `user: "${UID}:${GID}"` is set, or check the socket permissions on the host.

## Requirements

* **Go:** 1.22+
* **Docker Engine:** API version 1.40+
* **OS:** Linux, macOS (Docker Desktop), Windows (WSL2 recommended)

## License

MIT
