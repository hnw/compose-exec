// Package main demonstrates executing a command in a Compose service.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/hnw/compose-exec/compose"
)

const dockerSockPath = "/var/run/docker.sock"

func maybePrintRootlessHint(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	if os.IsPermission(err) ||
		(strings.Contains(msg, "permission denied") && strings.Contains(msg, "docker.sock")) {
		fmt.Fprintln(
			os.Stderr,
			"Hint: /var/run/docker.sock に書き込みできません。Rootless 環境 (lima/Colima 等) の可能性があります。",
		)
		fmt.Fprintln(
			os.Stderr,
			"Hint: docker.sock の bind mount / パーミッション設定 (例: Docker Desktop/Colima の設定や DOCKER_HOST) を見直してください。",
		)
	}
}

func diagnoseDockerSockWrite() {
	f, err := os.OpenFile(dockerSockPath, os.O_WRONLY, 0)
	if err == nil {
		_ = f.Close()
		return
	}
	maybePrintRootlessHint(err)
}

func main() {
	ctx := context.Background()

	// Rootless/VM 環境で /var/run/docker.sock が読めても書けず失敗するケースがあるため、事前に診断する。
	diagnoseDockerSockWrite()

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
		maybePrintRootlessHint(err)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("===========================")
}
