package compose

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultComposeFiles_UsesYamlWhenYmlMissing(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "docker-compose.yaml")
	if err := os.WriteFile(base, []byte("services: {}"), 0o600); err != nil {
		t.Fatalf("write base: %v", err)
	}

	files := defaultComposeFiles(dir, nil)
	if len(files) != 1 {
		t.Fatalf("files=%v", files)
	}
	if files[0] != base {
		t.Fatalf("base=%q want=%q", files[0], base)
	}
}

func TestDefaultComposeFiles_PrefersYmlOverYaml(t *testing.T) {
	dir := t.TempDir()
	yml := filepath.Join(dir, "docker-compose.yml")
	yaml := filepath.Join(dir, "docker-compose.yaml")
	if err := os.WriteFile(yml, []byte("services: {}"), 0o600); err != nil {
		t.Fatalf("write yml: %v", err)
	}
	if err := os.WriteFile(yaml, []byte("services: {}"), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	files := defaultComposeFiles(dir, nil)
	if len(files) != 1 {
		t.Fatalf("files=%v", files)
	}
	if files[0] != yml {
		t.Fatalf("base=%q want=%q", files[0], yml)
	}
}

func TestDefaultComposeFiles_UsesOverrideYaml(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "docker-compose.yaml")
	override := filepath.Join(dir, "docker-compose.override.yaml")
	if err := os.WriteFile(base, []byte("services: {}"), 0o600); err != nil {
		t.Fatalf("write base: %v", err)
	}
	if err := os.WriteFile(override, []byte("services: {}"), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}

	files := defaultComposeFiles(dir, nil)
	if len(files) != 2 {
		t.Fatalf("files=%v", files)
	}
	if files[0] != base || files[1] != override {
		t.Fatalf("files=%v want=[%q %q]", files, base, override)
	}
}
