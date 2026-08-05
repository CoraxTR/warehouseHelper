package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvFilePath(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Errorf("восстановление рабочего каталога: %v", err)
		}
	})

	dir := t.TempDir()
	envAbs := filepath.Join(dir, ".env")
	if err := os.WriteFile(envAbs, []byte("TEST=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("запуск из корня репозитория", func(t *testing.T) {
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		if got := envFilePath(); got != envAbs {
			t.Fatalf("envFilePath() = %q, want %q", got, envAbs)
		}
	})

	t.Run("запуск из каталога cmd/", func(t *testing.T) {
		cmdDir := filepath.Join(dir, "cmd")
		if err := os.MkdirAll(cmdDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(cmdDir); err != nil {
			t.Fatal(err)
		}
		if got := envFilePath(); got != envAbs {
			t.Fatalf("envFilePath() = %q, want %q", got, envAbs)
		}
	})

	t.Run("файл .env не найден", func(t *testing.T) {
		emptyDir := t.TempDir()
		if err := os.Chdir(emptyDir); err != nil {
			t.Fatal(err)
		}
		if got := envFilePath(); got != "" {
			t.Fatalf("envFilePath() = %q, want пустую строку", got)
		}
	})
}
