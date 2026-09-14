package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
				t.Errorf("parseEnvFloat() = (%v, %v), want (%v, %v)", got, tt.want, ok, tt.ok)
			}
		})
	}
}

// loadAppConfig проверяет дефолты и разбор настроек модуля скидок
// (ёмкости окна/слота, времена шагов ТГ-дня).
func TestLoadAppConfigDiscounts(t *testing.T) {
	tests := []struct {
		name          string
		windowCap     string
		telegramCap   string
		planTime      string
		raiseTime     string
		wantWindowCap int
		wantTelegram  int
		wantPlanTime  time.Duration
		wantRaiseTime time.Duration
	}{
		{
			name:          "пустые переменные — дефолты",
			wantWindowCap: 12,
			wantTelegram:  10,
			wantPlanTime:  14 * time.Hour,
			wantRaiseTime: 16 * time.Hour,
		},
		{
			name:          "валидные значения",
			windowCap:     "20",
			telegramCap:   "7",
			planTime:      "13:30",
			raiseTime:     "17:45",
			wantWindowCap: 20,
			wantTelegram:  7,
			wantPlanTime:  13*time.Hour + 30*time.Minute,
			wantRaiseTime: 17*time.Hour + 45*time.Minute,
		},
		{
			name:          "невалидные значения — дефолты",
			windowCap:     "abc",
			telegramCap:   "0",
			planTime:      "25:99",
			raiseTime:     "16-00",
			wantWindowCap: 12,
			wantTelegram:  10,
			wantPlanTime:  14 * time.Hour,
			wantRaiseTime: 16 * time.Hour,
		},
		{
			name:          "отрицательные ёмкости — дефолты",
			windowCap:     "-3",
			telegramCap:   "-1",
			wantWindowCap: 12,
			wantTelegram:  10,
			wantPlanTime:  14 * time.Hour,
			wantRaiseTime: 16 * time.Hour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// loadAppconfig завершает процесс без APP_HTTPADDRESS (обязательная
			// настройка приложения) — в тесте она не проверяется, но нужна.
			t.Setenv("APP_HTTPADDRESS", ":8080")
			t.Setenv("APP_DISCOUNT_WINDOW_CAP", tt.windowCap)
			t.Setenv("APP_DISCOUNT_TELEGRAM_CAP", tt.telegramCap)
			t.Setenv("APP_DISCOUNT_TG_PLAN_TIME", tt.planTime)
			t.Setenv("APP_DISCOUNT_TG_RAISE_TIME", tt.raiseTime)

			cfg := loadAppconfig()
			if cfg.DiscountWindowCap != tt.wantWindowCap {
				t.Errorf("DiscountWindowCap = %d, want %d", cfg.DiscountWindowCap, tt.wantWindowCap)
			}
			if cfg.DiscountTelegramCap != tt.wantTelegram {
				t.Errorf("DiscountTelegramCap = %d, want %d", cfg.DiscountTelegramCap, tt.wantTelegram)
			}
			if cfg.DiscountTGPlanTime != tt.wantPlanTime {
				t.Errorf("DiscountTGPlanTime = %v, want %v", cfg.DiscountTGPlanTime, tt.wantPlanTime)
			}
			if cfg.DiscountTGRaiseTime != tt.wantRaiseTime {
				t.Errorf("DiscountTGRaiseTime = %v, want %v", cfg.DiscountTGRaiseTime, tt.wantRaiseTime)
			}
		})
	}
}

func TestEnvFilePath(t *testing.T) {
	dir := t.TempDir()
	envAbs := filepath.Join(dir, ".env")
	if err := os.WriteFile(envAbs, []byte("TEST=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("запуск из корня репозитория", func(t *testing.T) {
		t.Chdir(dir)
		if got := envFilePath(); got != envAbs {
			t.Fatalf("envFilePath() = %q, want %q", got, envAbs)
		}
	})

	t.Run("запуск из каталога cmd/", func(t *testing.T) {
		cmdDir := filepath.Join(dir, "cmd")
		if err := os.MkdirAll(cmdDir, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Chdir(cmdDir)
		if got := envFilePath(); got != envAbs {
			t.Fatalf("envFilePath() = %q, want %q", got, envAbs)
		}
	})

	t.Run("файл .env не найден", func(t *testing.T) {
		emptyDir := t.TempDir()
		t.Chdir(emptyDir)
		if got := envFilePath(); got != "" {
			t.Fatalf("envFilePath() = %q, want пустую строку", got)
		}
	})
}
