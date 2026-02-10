// Package main demonstrates using compose-exec to run and supervise a service.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/hnw/compose-exec/compose"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverCmd := `
		echo "[Target] Initializing Backend Service...";
		sleep 3;
		echo "[Target] Starting TCP Listener (Auto-shutdown in 20s)...";

		timeout 20s sh -c 'while true; do nc -lk -p 8080 -e cat || nc -l -p 8080; sleep 0.1; done'
	`

	fmt.Println("[Controller] Launching 'Slow-Start' Target Container...")
	cmd := compose.CommandContext(ctx, "target", "sh", "-c", serverCmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	defer func() {
		fmt.Println("\n[Controller] Cleaning up target container...")
		cancel()
		_ = cmd.Wait()
		fmt.Println("[Controller] Cleanup done.")
	}()

	if err := cmd.Start(); err != nil {
		panic(err)
	}

	targetAddr := "target:8080"
	fmt.Println("[Controller] 1. Attempting IMMEDIATE connection (Expect FAILURE)...")

	failConn, err := net.DialTimeout("tcp", targetAddr, 500*time.Millisecond)
	if err == nil {
		_ = failConn.Close()
		fmt.Println("Error: Unexpectedly connected! The demo scenario is broken.")
		os.Exit(1)
	}
	fmt.Printf("   -> As expected, connection failed: %v\n", err)

	fmt.Println("[Controller] 2. Waiting for Target (Port 8080) to be Ready...")
	startWait := time.Now()

	if waitErr := cmd.WaitUntilHealthy(); waitErr != nil {
		panic(waitErr)
	}

	elapsed := time.Since(startWait).Round(time.Millisecond)
	fmt.Printf("   -> Target is HEALTHY! Waited: %v\n", elapsed)

	fmt.Printf("[Controller] 3. Connecting to %s ... ", targetAddr)

	conn, err := net.DialTimeout("tcp", targetAddr, 2*time.Second)
	if err != nil {
		fmt.Printf("FAILED: %v\n", err)
		os.Exit(1)
	}
	_ = conn.Close()
	fmt.Println("SUCCESS! (Connection established)")
}
