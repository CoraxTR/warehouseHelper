package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"warehouseHelper/internal/config"
)

// SetCommands отправляет список команд в setMyCommands: путь метода, имя без
// слэша, описание. Это меню, которое клиенты показывают по нажатию «/».
func TestSetCommandsSendsList(t *testing.T) {
	var gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path

		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testNotifier(&config.TelegramConfig{BotToken: "test-token"}, srv.URL)

	cmds := []BotCommand{{Command: "discounts", Description: "Актуальный отчёт по скидкам"}}
	if err := n.SetCommands(context.Background(), cmds); err != nil {
		t.Fatalf("SetCommands error: %v", err)
	}

	if gotPath != "/bottest-token/setMyCommands" {
		t.Errorf("path = %q, want /bottest-token/setMyCommands", gotPath)
	}

	raw, ok := gotBody["commands"].([]any)
	if !ok || len(raw) != 1 {
		t.Fatalf("commands = %#v, want список из одной команды", gotBody["commands"])
	}

	first, ok := raw[0].(map[string]any)
	if !ok {
		t.Fatalf("commands[0] = %#v, want объект", raw[0])
	}

	if first["command"] != "discounts" {
		t.Errorf("command = %v, want discounts", first["command"])
	}

	if first["description"] != "Актуальный отчёт по скидкам" {
		t.Errorf("description = %v, want %q", first["description"], "Актуальный отчёт по скидкам")
	}
}

// Без токена (телеграм не настроен) — ни запроса, ни ошибки: меню некому задавать.
func TestSetCommandsDisabledWithoutToken(t *testing.T) {
	hit := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testNotifier(&config.TelegramConfig{}, srv.URL)

	cmds := []BotCommand{{Command: "discounts", Description: "Актуальный отчёт по скидкам"}}
	if err := n.SetCommands(context.Background(), cmds); err != nil {
		t.Fatalf("SetCommands без токена: %v", err)
	}

	if hit {
		t.Error("без токена запрос к Bot API не ожидается")
	}
}

// Пустой список — тоже no-op: setMyCommands замещает список целиком, и пустым
// вызовом легко стереть меню, настроенное в BotFather.
func TestSetCommandsEmptyListNoRequest(t *testing.T) {
	hit := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testNotifier(&config.TelegramConfig{BotToken: "test-token"}, srv.URL)

	if err := n.SetCommands(context.Background(), nil); err != nil {
		t.Fatalf("SetCommands с пустым списком: %v", err)
	}

	if hit {
		t.Error("с пустым списком запрос к Bot API не ожидается")
	}
}

// Ошибку Bot API метод возвращает наружу: решение «логировать и работать дальше»
// принимает вызывающий (в приложении — фоновая задача меню команд).
func TestSetCommandsNon2xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: command name invalid"}`))
	}))
	defer srv.Close()

	n := testNotifier(&config.TelegramConfig{BotToken: "test-token"}, srv.URL)

	cmds := []BotCommand{{Command: "discounts", Description: "Актуальный отчёт по скидкам"}}
	if err := n.SetCommands(context.Background(), cmds); err == nil {
		t.Fatal("SetCommands error = nil, want non-nil on 400")
	}
}
