package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"warehouseHelper/internal/config"
)

// Тесты модуля returns: сообщение складу с URL-кнопкой «Расформировать»
// (возврат message_id для удаления) и deleteMessage обработанного сообщения.

func TestSendWarehouseReturnSendsURLButton(t *testing.T) {
	var (
		gotPath string
		gotBody map[string]any
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42}}`))
	}))
	defer srv.Close()

	n := testNotifier(&config.TelegramConfig{
		BotToken:        "test-token",
		WarehouseChatID: -100999,
	}, srv.URL)

	chatID, messageID, err := n.SendWarehouseReturn(context.Background(),
		"Заказ 19379 был переведён в статус «Отменён»",
		"http://warehouse.local:8080/goods/return?e=884ff854-abc1-11f1-0a80-106a00019ed6")
	if err != nil {
		t.Fatalf("SendWarehouseReturn error: %v", err)
	}

	if gotPath != "/bottest-token/sendMessage" {
		t.Errorf("path = %q, want /bottest-token/sendMessage", gotPath)
	}
	if chatID != -100999 {
		t.Errorf("chatID = %d, want -100999", chatID)
	}
	if messageID != 42 {
		t.Errorf("messageID = %d, want 42 (из result.message_id)", messageID)
	}
	if gotBody["chat_id"] != float64(-100999) {
		t.Errorf("chat_id = %v, want -100999", gotBody["chat_id"])
	}
	if gotBody["parse_mode"] != nil {
		t.Errorf("parse_mode = %v, want nil (обычный текст, без HTML)", gotBody["parse_mode"])
	}

	markup, ok := gotBody["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("reply_markup = %#v, want object", gotBody["reply_markup"])
	}
	rows, ok := markup["inline_keyboard"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("inline_keyboard = %#v, want one row", markup["inline_keyboard"])
	}
	buttons, ok := rows[0].([]any)
	if !ok || len(buttons) != 1 {
		t.Fatalf("inline_keyboard row = %#v, want one button", rows[0])
	}
	btn, ok := buttons[0].(map[string]any)
	if !ok {
		t.Fatalf("button = %#v, want map", buttons[0])
	}
	if btn["text"] != "Расформировать" {
		t.Errorf("button text = %v, want «Расформировать»", btn["text"])
	}
	if btn["url"] != "http://warehouse.local:8080/goods/return?e=884ff854-abc1-11f1-0a80-106a00019ed6" {
		t.Errorf("button url = %v, want href страницы возврата", btn["url"])
	}
	if btn["callback_data"] != nil {
		t.Errorf("callback_data = %v, want nil (URL-кнопка)", btn["callback_data"])
	}
}

func TestSendWarehouseReturnNoOpWithoutChat(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testNotifier(&config.TelegramConfig{BotToken: "test-token"}, srv.URL)
	chatID, messageID, err := n.SendWarehouseReturn(context.Background(), "text", "http://x/")
	if err != nil {
		t.Fatalf("SendWarehouseReturn error: %v", err)
	}
	if hit {
		t.Error("no HTTP request expected without warehouse chat id")
	}
	if chatID != 0 || messageID != 0 {
		t.Errorf("chatID/messageID = %d/%d, want 0/0", chatID, messageID)
	}
}

func TestDeleteMessage(t *testing.T) {
	var (
		gotPath string
		gotBody map[string]any
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42}}`))
	}))
	defer srv.Close()

	n := testNotifier(&config.TelegramConfig{BotToken: "test-token"}, srv.URL)

	if err := n.DeleteMessage(context.Background(), -100999, 42); err != nil {
		t.Fatalf("DeleteMessage error: %v", err)
	}

	if gotPath != "/bottest-token/deleteMessage" {
		t.Errorf("path = %q, want /bottest-token/deleteMessage", gotPath)
	}
	if gotBody["chat_id"] != float64(-100999) {
		t.Errorf("chat_id = %v, want -100999", gotBody["chat_id"])
	}
	if gotBody["message_id"] != float64(42) {
		t.Errorf("message_id = %v, want 42", gotBody["message_id"])
	}
}

func TestDeleteMessageNoOpWithoutToken(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testNotifier(&config.TelegramConfig{}, srv.URL)
	if err := n.DeleteMessage(context.Background(), -100999, 42); err != nil {
		t.Fatalf("DeleteMessage error: %v", err)
	}
	if hit {
		t.Error("no HTTP request expected without bot token")
	}
}
