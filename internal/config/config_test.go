package config

import (
	"net"
	"net/netip"
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

// Адрес ссылок в уведомлениях: старшинство источников и сборка из хоста и порта
// (COMPLAINTS_PUBLIC_URL → APP_PUBLIC_HOST → локальный IPv4 → mDNS-дефолт).
func TestPublicURL(t *testing.T) {
	tests := []struct {
		name      string
		publicURL string
		host      string
		httpAddr  string
		want      string
	}{
		{
			name:      "COMPLAINTS_PUBLIC_URL старше хоста",
			publicURL: " http://192.168.1.50:9000 ",
			host:      "192.168.1.71",
			httpAddr:  ":8080",
			want:      "http://192.168.1.50:9000",
		},
		{
			name:     "хост из env, порт из APP_HTTPADDRESS",
			host:     "192.168.1.71",
			httpAddr: ":8080",
			want:     "http://192.168.1.71:8080",
		},
		{
			name:     "порт берётся из адреса прослушивания",
			host:     "warehouse",
			httpAddr: "0.0.0.0:9000",
			want:     "http://warehouse:9000",
		},
		{
			name:     "пробелы вокруг хоста отбрасываются",
			host:     " 192.168.1.71 ",
			httpAddr: "  :8080 ",
			want:     "http://192.168.1.71:8080",
		},
		{
			name:     "хост с портом — порт не дублируется",
			host:     "192.168.1.71:9000",
			httpAddr: ":8080",
			want:     "http://192.168.1.71:9000",
		},
		{
			name:     "IPv6-хост в квадратные скобки",
			host:     "::1",
			httpAddr: ":8080",
			want:     "http://[::1]:8080",
		},
		{
			name:     "порт не задан — прежний адрес",
			host:     "192.168.1.71",
			httpAddr: "192.168.1.71",
			want:     defaultPublicURL,
		},
		{
			name:     "порт не число — прежний адрес",
			host:     "192.168.1.71",
			httpAddr: ":http",
			want:     defaultPublicURL,
		},
		{
			name:     "порт вне диапазона — прежний адрес",
			host:     "192.168.1.71",
			httpAddr: ":70000",
			want:     defaultPublicURL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("COMPLAINTS_PUBLIC_URL", tt.publicURL)
			t.Setenv("APP_PUBLIC_HOST", tt.host)
			t.Setenv("APP_HTTPADDRESS", tt.httpAddr)

			if got := publicURL(); got != tt.want {
				t.Errorf("publicURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Без APP_PUBLIC_HOST адрес собирается из локального IPv4 машины: значение
// зависит от машины, поэтому ожидание строится от localIPv4.
func TestPublicURLFromLocalIPv4(t *testing.T) {
	t.Setenv("COMPLAINTS_PUBLIC_URL", "")
	t.Setenv("APP_PUBLIC_HOST", "")
	t.Setenv("APP_HTTPADDRESS", ":8080")

	want := defaultPublicURL
	if host := localIPv4(); host != "" {
		want = "http://" + net.JoinHostPort(host, "8080")
	}

	if got := publicURL(); got != want {
		t.Errorf("publicURL() = %q, want %q", got, want)
	}
}

// Порт команды ссылки: берётся из APP_HTTPADDRESS, мусор и выход за диапазон
// портов отбрасываются.
func TestHTTPPort(t *testing.T) {
	tests := []struct {
		name     string
		httpAddr string
		want     string
	}{
		{name: "только порт", httpAddr: ":8080", want: "8080"},
		{name: "все интерфейсы", httpAddr: "0.0.0.0:8080", want: "8080"},
		{name: "конкретный адрес", httpAddr: "192.168.1.71:9000", want: "9000"},
		{name: "максимальный порт", httpAddr: ":65535", want: "65535"},
		{name: "пустое значение", httpAddr: "", want: ""},
		{name: "без порта", httpAddr: "192.168.1.71", want: ""},
		{name: "порт не число", httpAddr: ":abc", want: ""},
		{name: "нулевой порт", httpAddr: ":0", want: ""},
		{name: "порт вне диапазона", httpAddr: ":65536", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("APP_HTTPADDRESS", tt.httpAddr)

			if got := httpPort(); got != tt.want {
				t.Errorf("httpPort() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Выбор адреса для ссылок: частный адрес предпочтительнее прочего, loopback,
// link-local и IPv6 не годятся.
func TestFirstUsableIPv4(t *testing.T) {
	tests := []struct {
		name  string
		addrs []string
		want  string
	}{
		{
			name:  "частный старше публичного",
			addrs: []string{"8.8.8.8", "192.168.1.71"},
			want:  "192.168.1.71",
		},
		{
			name:  "единственный частный",
			addrs: []string{"127.0.0.1", "169.254.10.1", "10.0.0.5"},
			want:  "10.0.0.5",
		},
		{
			name:  "публичный, когда частного нет",
			addrs: []string{"169.254.10.1", "8.8.8.8"},
			want:  "8.8.8.8",
		},
		{
			name:  "IPv6 не годится",
			addrs: []string{"::1", "fe80::1", "2001:db8::1"},
			want:  "",
		},
		{
			name:  "только непригодные",
			addrs: []string{"127.0.0.1", "169.254.1.1"},
			want:  "",
		},
		{
			name:  "список пуст",
			addrs: nil,
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addrs := make([]netip.Addr, 0, len(tt.addrs))
			for _, s := range tt.addrs {
				addrs = append(addrs, netip.MustParseAddr(s))
			}

			got, ok := firstUsableIPv4(addrs)

			want := netip.Addr{}
			if tt.want != "" {
				want = netip.MustParseAddr(tt.want)
			}
			if ok != (tt.want != "") || got != want {
				t.Errorf("firstUsableIPv4() = (%v, %v), want (%v, %v)", got, ok, want, tt.want != "")
			}
		})
	}
}
