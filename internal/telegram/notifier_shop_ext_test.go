package telegram

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"warehouseHelper/internal/config"
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
