package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseEnvFloat(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  float64
		ok    bool
	}{
		{"целое", "2", 2, true},
		{"дробное через точку", "2.5", 2.5, true},
		{"дробное через запятую", "2,5", 2.5, true},
		{"пустая переменная", "", 0, false},
		{"не число", "abc", 0, false},
		{"пробелы вокруг", "  3.5 ", 3.5, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("RG_TEST_FLOAT", tt.value)

			got, ok := parseEnvFloat("RG_TEST_FLOAT")
			if got != tt.want || ok != tt.ok {
				t.Errorf("parseEnvFloat() = (%v, %v), want (%v, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

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
