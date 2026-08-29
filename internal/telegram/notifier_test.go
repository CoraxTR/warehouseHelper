package telegram

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"warehouseHelper/internal/config"
)

// testNotifier собирает Notifier с переопределённым базовым URL.
func testNotifier(cfg *config.TelegramConfig, baseURL string) *Notifier {
	n := NewNotifier(cfg)
	n.apiBaseURL = baseURL

	return n
}

func TestNotifyWarehouseSendsMessage(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method

		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testNotifier(&config.TelegramConfig{
		BotToken:        "test-token",
		WarehouseChatID: -1001234567890,
	}, srv.URL)

	if err := n.NotifyWarehouse("Привет, склад"); err != nil {
		t.Fatalf("NotifyWarehouse error: %v", err)
	}

	if gotPath != "/bottest-token/sendMessage" {
		t.Errorf("path = %q, want /bottest-token/sendMessage", gotPath)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}

	if gotBody["chat_id"] != float64(-1001234567890) {
		t.Errorf("chat_id = %v, want -1001234567890", gotBody["chat_id"])
	}

	if gotBody["text"] != "Привет, склад" {
		t.Errorf("text = %v, want %q", gotBody["text"], "Привет, склад")
	}
}

func TestNotifyWarehouseNon2xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false}`))
	}))
	defer srv.Close()

	n := testNotifier(&config.TelegramConfig{
		BotToken:        "test-token",
		WarehouseChatID: -1001234567890,
	}, srv.URL)

	if err := n.NotifyWarehouse("test"); err == nil {
		t.Fatal("NotifyWarehouse error = nil, want non-nil on 400")
	}
}

func TestNotifyWarehouseDisabledWithoutConfig(t *testing.T) {
	hit := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testNotifier(&config.TelegramConfig{}, srv.URL)

	if err := n.NotifyWarehouse("test"); err != nil {
		t.Fatalf("NotifyWarehouse disabled error: %v", err)
	}

	if err := n.NotifyEveryone("test"); err != nil {
		t.Fatalf("NotifyEveryone disabled error: %v", err)
	}

	if hit {
		t.Error("no HTTP request expected when telegram is not configured")
	}
}

func TestNotifyEveryoneSendsToEveryoneChat(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testNotifier(&config.TelegramConfig{
		BotToken:       "test-token",
		EveryoneChatID: -1009998887777,
	}, srv.URL)

	if err := n.NotifyEveryone("Всем привет"); err != nil {
		t.Fatalf("NotifyEveryone error: %v", err)
	}

	if gotBody["chat_id"] != float64(-1009998887777) {
		t.Errorf("chat_id = %v, want -1009998887777", gotBody["chat_id"])
	}
}
