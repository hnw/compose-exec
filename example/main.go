// Package main demonstrates executing a command in a Compose service.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/hnw/compose-exec/compose"
)

func main() {
	ctx := context.Background()

	fmt.Println("=== 1. Controller (Self) ===")
	// 自分自身のOS情報を表示 (Debian)
	cmd := exec.Command("cat", "/etc/os-release")
	cmd.Stdout = os.Stdout
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("============================")

	fmt.Println()

	fmt.Println("=== 2. Sibling (Target) ===")
	// Siblingコンテナを起動してOS情報を表示 (Alpine)
	// docker-compose.yml の "sibling" サービスを使用
	siblingCmd := compose.From("sibling").Command("cat", "/etc/os-release")
	siblingCmd.Stdout = os.Stdout
	siblingCmd.Stderr = os.Stderr

	if err := siblingCmd.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("===========================")
}
