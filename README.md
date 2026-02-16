# compose-exec

[![Go Reference](https://pkg.go.dev/badge/github.com/hnw/compose-exec.svg)](https://pkg.go.dev/github.com/hnw/compose-exec)
[Japanese README (日本語ドキュメント)](./README_ja.md)

**Run Docker Compose services like `os/exec`. No Docker CLI required.**

`compose-exec` is a Go library that manages the lifecycle of containers directly via the Docker Engine API, using your `docker-compose.yml` as the definition.
It eliminates the need for the `docker` binary and shell scripts, providing a safer, programmable alternative for container automation.

## 🎯 Primary Use Case: ChatOps / AI Agents

You have a Go-based bot or agent running in a container, and it needs to execute many tools.
Bundling binaries for every tool grows the image and complicates updates; shelling out to `docker compose` adds surface area and operational complexity.

With `compose-exec`, each tool is a Compose service (a sibling container), and you call it with the same `os/exec`-style interface.

* Keep one small controller binary; add tools by editing `docker-compose.yml`.
* Run tools in isolated containers instead of embedding binaries.
* Tie container lifecycle to `context.Context` and avoid orphaned containers.

## 🧭 How it works

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

    Lib -- "1. Load Config" --> File
    Lib -- "2. API Call (Socket)" --> Daemon
    Daemon -- "3. Spawn (DooD)" --> Target

    class Host host;
    class Controller container;

```

## 📖 Usage (Integration Testing)

Example of using an existing `docker-compose.yml` to start a database and wait for it to be healthy before running tests.
The same pattern applies to ChatOps: treat each service as a command target and call it via `Command()`.

```go
package main

import (
	"context"
	"fmt"
	"os"
	"github.com/hnw/compose-exec/compose"
)

func main() {
	// Context to manage container lifecycle
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Define command bound to the "db" service (Empty args = use image default command)
	// Bind lifecycle to context
	cmd := compose.CommandContext(ctx, "db")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// 2. Start the container asynchronously
	if err := cmd.Start(); err != nil {
		panic(err)
	}

	// Ensure container is removed when function exits
	defer cmd.Wait()

	// 3. ✨ Wait for Healthcheck
	// Uses the healthcheck defined in your YAML. No more arbitrary "sleep 10".
	fmt.Println("Waiting for DB to be healthy...")
	if err := cmd.WaitUntilHealthy(); err != nil {
		panic(err)
	}

	// 4. Run your tests or logic
	fmt.Println("DB is ready! Running tests...")
	// runTests()
}

```

## 🏃 Try it now (Sibling Container Demo)

This repository itself serves as a functional demo.
Run the following to see the "Controller" container dynamically spawn and control a "Sibling" container. No Go installation required.

```bash
# Clone and run
git clone https://github.com/hnw/compose-exec.git
cd compose-exec
docker compose run controller

```

Execution Output

```text
[Controller] Launching 'Slow-Start' Target Container...
[Controller] 1. Attempting IMMEDIATE connection (Expect FAILURE)...
   -> As expected, connection failed: dial tcp: lookup target: no such host
[Controller] 2. Waiting for Target (Port 8080) to be Ready...
   -> Target is HEALTHY! Waited: 3.2s
[Controller] 3. Connecting to target:8080 ... SUCCESS!

```

This demonstrates the **DooD (Docker outside of Docker)** pattern, often used in CI environments.

## ✨ Why compose-exec?

* **No Docker Binary Required:**
Runs without the `docker` CLI installation. Compatible with `distroless` or `scratch` images.
* **Robust Lifecycle Management:**
Strictly ties container lifecycle to your Go `Context`. If your program panics or times out, containers are cleaned up ensuring no zombie processes.
* **Secure & Injection-Proof:**
Avoids shell execution entirely. By using the API directly, it structurally eliminates OS command injection risks.
Ideal for building secure **ChatOps bots** or **AI Agent sandboxes**.
* **Compose as a Tool Registry:**
Add, upgrade, or swap tools by editing services in `docker-compose.yml` instead of shipping new binaries.

## ⚠️ Limitations / Compatibility

* `build` is not supported. `service.image` is required.
* Supported volume types are `bind` and `volume` only.
* This is not a full Docker Compose implementation. Only a subset of fields are applied
  (image, command, entrypoint, environment, ports, volumes, networks, healthcheck, user, init, privileged, cap_add/cap_drop, security_opt, shm_size, extra_hosts, devices, cpus, cpu_shares, cpuset).
* TTY is not supported.

## ⚙️ Configuration (DooD Setup)

When running this library inside a container (Docker-outside-of-Docker), you must configure the volume mounts correctly.

**Mirror Mounting** is essential. You must map the host's current directory to the exact same path inside the container so that the Docker Daemon (running on the host) can resolve relative paths and bind mounts defined in your Compose file.

**docker-compose.yml (Controller Example):**

```yaml
services:
  controller:
    image: golang:1.24
    volumes:
      # 1. Access Docker API (Required)
      - /var/run/docker.sock:/var/run/docker.sock

      # 2. Mirror Mount (Required)
      # Map the host working dir (${PWD}) to the same path inside the container.
      - .:${PWD}

    # 3. Match Working Directory
    working_dir: ${PWD}

```

## Installation

```bash
go get github.com/hnw/compose-exec

```

## Requirements

* **Go:** 1.24+
* **Docker Engine:** API v1.40+
* **OS:** Linux, macOS, Windows (WSL2 recommended)

## License

MIT
