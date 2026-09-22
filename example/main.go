// Package main demonstrates treating a Docker Compose service as an
// external command: Pandoc runs in its own container, invoked from Go.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/hnw/compose-exec/compose"
)

func main() {
	// The controller container mirrors the repository root at the same
	// path, so compose.CommandContext finds the root compose.yaml.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fmt.Println("[Controller] Converting Markdown to HTML...")
	fmt.Println("[Controller] Running Pandoc via the \"pandoc\" Compose service.")
	fmt.Println()
	fmt.Println("Input: example/input.md")
	fmt.Println()

	cmd := compose.CommandContext(ctx, "pandoc", "input.md", "-t", "html")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "\n[Controller] Pandoc failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\n[Controller] Done. Pandoc ran in a separate container,")
	fmt.Println("[Controller] so it is not installed in the controller image.")
}
