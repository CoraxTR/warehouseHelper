package telegram

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"warehouseHelper/internal/config"
	"warehouseHelper/internal/discounts"
)

// capturedRequest — что именно бот отправил в Bot API: путь метода и тело.
type capturedRequest struct {
	Path string
	Body map[string]any
}

// captureAPI — сервер Bot API, который запоминает последний запрос: по нему
// проверяется payload сообщения.
func captureAPI(t *testing.T) (*httptest.Server, *capturedRequest) {
	t.Helper()

	req := &capturedRequest{Body: map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req.Path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&req.Body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	return srv, req
}

// previewDisabled — выключен ли предпросмотр ссылок в теле запроса.
func previewDisabled(t *testing.T, body map[string]any) bool {
	t.Helper()

	raw, ok := body["link_preview_options"]
	if !ok {
		return false
	}
	opts, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("link_preview_options не объект: %T", raw)
	}
	disabled, ok := opts["is_disabled"].(bool)
	if !ok {
		t.Fatalf("is_disabled не bool: %T", opts["is_disabled"])
	}

	return disabled
}

// Пост чата скидок уходит без предпросмотра ссылок: иначе Telegram подклеивает
// карточку первой ссылки блока — снизу поста висит чужая позиция (инцидент
// 22.10.2026). Правило держится и на правке: превью не должно достраиваться.
func TestShopPostWithoutLinkPreview(t *testing.T) {
	srv, req := captureAPI(t)
	n := testNotifier(&config.TelegramConfig{BotToken: "test-token", DiscountChatID: -1007777777777}, srv.URL)

	if _, err := n.NotifyShopHTML("пост"); err != nil {
		t.Fatalf("публикация поста: %v", err)
	}
	if req.Path != "/bottest-token/sendMessage" {
		t.Errorf("path = %q, want sendMessage", req.Path)
	}
	if !previewDisabled(t, req.Body) {
		t.Error("в публикации поста предпросмотр ссылок не выключен")
	}

	req.Body = map[string]any{}
	if err := n.EditShopHTML(77, "пост после правки"); err != nil {
		t.Fatalf("правка поста: %v", err)
	}
	if req.Path != "/bottest-token/editMessageText" {
		t.Errorf("path = %q, want editMessageText", req.Path)
	}
	if !previewDisabled(t, req.Body) {
		t.Error("в правке поста предпросмотр ссылок не выключен")
	}
}

// Запрос фото уходит БЕЗ force_reply: принудительный ответ уводил в цитату к
// запросу важные сообщения склада (решение владельца 22.10.2026). Привязка фото
// к позиции — по-прежнему reply_to_message, поэтому «ответом на это сообщение»
// стоит в тексте запроса (см. discounts.PhotoRequest). Фото без ответа
// игнорируется — решение владельца.
func TestPhotoRequestHasNoForcedReply(t *testing.T) {
	srv, req := captureAPI(t)
	n := testNotifier(&config.TelegramConfig{BotToken: "test-token", WarehouseChatID: -1001234567890}, srv.URL)

	if _, err := n.NotifyWarehouseReplyRequest("Сделайте фото для Колбаса"); err != nil {
		t.Fatalf("запрос фото: %v", err)
	}
	if req.Path != "/bottest-token/sendMessage" {
		t.Errorf("path = %q, want sendMessage", req.Path)
	}
	if markup, ok := req.Body["reply_markup"]; ok {
		t.Errorf("у запроса фото остался reply_markup: %v", markup)
	}
}

// Отказ правки поста разводится по смыслу: «не найдено» — пост удалён, юзкейс
// публикует новый; «нельзя править» — это права администратора, и повторная
// публикация там дала бы дубль в канале. Признак у Bot API один — текст ответа.
func TestShopPostEditErrorClass(t *testing.T) {
	tests := []struct {
		name        string
		description string
		want        error
		not         error
	}{
		{"пост удалён", "Bad Request: message to edit not found", discounts.ErrShopPostGone, discounts.ErrShopPostNotEditable},
		{"править нельзя", "Bad Request: message can't be edited", discounts.ErrShopPostNotEditable, discounts.ErrShopPostGone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				if _, err := fmt.Fprintf(w, `{"ok":false,"error_code":400,"description":%q}`, tt.description); err != nil {
					t.Errorf("ответ Bot API: %v", err)
				}
			}))
			t.Cleanup(srv.Close)

			n := testNotifier(&config.TelegramConfig{BotToken: "test-token", DiscountChatID: -1007777777777}, srv.URL)
			err := n.EditShopHTML(77, "пост после правки")
			if !errors.Is(err, tt.want) {
				t.Fatalf("ошибка правки = %v, want %v", err, tt.want)
			}
			if errors.Is(err, tt.not) {
				t.Errorf("отказы склеены: %v", err)
			}
		})
	}
}
