package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"time"

	"warehouseHelper/internal/config"
	"warehouseHelper/internal/domain"
)

const (
	// telegramAPIBase — базовый URL Bot API; переопределяется в тестах.
	telegramAPIBase = "https://api.telegram.org"
	sendTimeout     = 15 * time.Second

	// mediaGroupMaxPhotos — лимит Telegram на вложений в одном sendMediaGroup.
	mediaGroupMaxPhotos = 10
	// telegramPhotoMaxBytes — фото (type=photo) принимается до 10 МБ;
	// больше — слать документом (до 50 МБ).
	telegramPhotoMaxBytes = 10 << 20
	telegramDocMaxBytes   = 50 << 20
)

// Notifier отправляет сообщения в чаты Telegram. Если токен бота или
// целевой chat_id не настроены (пустой токен / нулевой id), соответствующий
// метод молча возвращает nil — уведомления отключены, приложение не падает.
type Notifier struct {
	httpClient      *http.Client
	apiBaseURL      string
	botToken        string
	warehouseChatID int64
	everyoneChatID  int64
	commonChatID    int64
}

// NewNotifier собирает Notifier из конфигурации Telegram.
func NewNotifier(cfg *config.TelegramConfig) *Notifier {
	return &Notifier{
		httpClient:      &http.Client{Timeout: sendTimeout},
		apiBaseURL:      telegramAPIBase,
		botToken:        cfg.BotToken,
		warehouseChatID: cfg.WarehouseChatID,
		everyoneChatID:  cfg.EveryoneChatID,
		commonChatID:    cfg.CommonChatID,
	}
}

// NotifyWarehouse отправляет сообщение в чат склада.
// Без настроенного токена или chat_id склада — no-op.
func (n *Notifier) NotifyWarehouse(text string) error {
	if n.botToken == "" || n.warehouseChatID == 0 {
		return nil
	}

	return n.sendMessage(context.Background(), n.warehouseChatID, text)
}

// NotifyEveryone отправляет сообщение в общий чат сотрудников.
// Без настроенного токена или chat_id общего чата — no-op.
func (n *Notifier) NotifyEveryone(text string) error {
	if n.botToken == "" || n.everyoneChatID == 0 {
		return nil
	}

	return n.sendMessage(context.Background(), n.everyoneChatID, text)
}

// NotifyCommon отправляет сообщение в общий канал (уведомления модулей,
// например смена наличия товара). Без настроенного токена или chat_id
// общего канала — no-op.
func (n *Notifier) NotifyCommon(ctx context.Context, text string) error {
	if n.botToken == "" || n.commonChatID == 0 {
		return nil
	}

	return n.sendMessage(ctx, n.commonChatID, text)
}

// NotifyCommonStatus отправляет в общий канал HTML-сообщение с inline-кнопкой
// (жалобы: уведомление о статусе + кнопка «Получить подробности»).
// callbackData — данные кнопки; текст кнопки фиксированный. Без настроенного
// токена или chat_id общего канала — no-op.
func (n *Notifier) NotifyCommonStatus(ctx context.Context, textHTML, callbackData string) error {
	if n.botToken == "" || n.commonChatID == 0 {
		return nil
	}

	payload := map[string]any{
		"chat_id":    n.commonChatID,
		"text":       textHTML,
		"parse_mode": "HTML",
		"reply_markup": map[string]any{
			"inline_keyboard": [][]map[string]string{{
				{"text": "Получить подробности", "callback_data": callbackData},
			}},
		},
	}
	return n.postJSON(ctx, "sendMessage", payload)
}

// SendDetails отправляет обычное текстовое сообщение в указанный чат
// (без parse_mode — пользовательский текст, HTML там не размечается).
// Без токена или chat_id — no-op.
func (n *Notifier) SendDetails(ctx context.Context, chatID int64, text string) error {
	if n.botToken == "" || chatID == 0 {
		return nil
	}
	return n.sendMessage(ctx, chatID, text)
}

// AnswerCallback закрывает callback-query («часики» на inline-кнопке).
// Без токена — no-op.
func (n *Notifier) AnswerCallback(ctx context.Context, callbackQueryID string) error {
	if n.botToken == "" || callbackQueryID == "" {
		return nil
	}
	return n.postJSON(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": callbackQueryID,
	})
}

// SendPhotos отправляет фотографии в указанный чат media-группами
// (до mediaGroupMaxPhotos за сообщение). Фото jpg/png/webp/gif до 10 МБ
// уходят как photo; крупные файлы и форматы без нативной поддержки
// (heic/heif) — документами (до 50 МБ); больше лимита — пропускаются
// с логом. Без токена или chat_id — no-op.
func (n *Notifier) SendPhotos(ctx context.Context, chatID int64, photos []domain.ComplaintTGPhoto) error {
	if n.botToken == "" || chatID == 0 || len(photos) == 0 {
		return nil
	}

	for start := 0; start < len(photos); start += mediaGroupMaxPhotos {
		end := min(start+mediaGroupMaxPhotos, len(photos))
		if err := n.sendMediaGroup(ctx, chatID, photos[start:end]); err != nil {
			return fmt.Errorf("send media group %d..%d: %w", start, end, err)
		}
	}
	return nil
}

// sendMediaGroup отправляет одну media-группу (до 10 вложений) через
// multipart-запрос: поле media — JSON со ссылками attach://fileN,
// сами файлы — частями fileN.
func (n *Notifier) sendMediaGroup(ctx context.Context, chatID int64, photos []domain.ComplaintTGPhoto) error {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	if err := mw.WriteField("chat_id", fmt.Sprintf("%d", chatID)); err != nil {
		return fmt.Errorf("multipart chat_id: %w", err)
	}

	type mediaItem struct {
		Type  string `json:"type"`
		Media string `json:"media"`
	}
	items := make([]mediaItem, 0, len(photos))
	for i, p := range photos {
		kind, ok := photoKind(p)
		if !ok {
			slog.Info(fmt.Sprintf("telegram: фото %d байт не влезает в лимиты Bot API, пропущено", len(p.Data)))
			continue
		}
		attach := fmt.Sprintf("file%d", i)
		items = append(items, mediaItem{Type: kind, Media: "attach://" + attach})

		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, attach, fmt.Sprintf("photo%d.%s", i, p.Ext)))
		h.Set("Content-Type", "application/octet-stream")
		part, err := mw.CreatePart(h)
		if err != nil {
			return fmt.Errorf("multipart part %s: %w", attach, err)
		}
		if _, err := part.Write(p.Data); err != nil {
			return fmt.Errorf("multipart write %s: %w", attach, err)
		}
	}
	if len(items) == 0 {
		return nil // все фото превысили лимиты — нечего отправлять
	}

	mediaJSON, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("marshal media group: %w", err)
	}
	if err := mw.WriteField("media", string(mediaJSON)); err != nil {
		return fmt.Errorf("multipart media: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("multipart close: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendMediaGroup", n.apiBaseURL, n.botToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return fmt.Errorf("failed to create telegram request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram request failed: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Error(fmt.Sprintf("failed to close telegram response body: %v", err))
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodySnippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("telegram API returned %s: %s", resp.Status, string(bodySnippet))
	}
	return nil
}

// photoKind решает, чем отправить фото в media-группе: photo (до 10 МБ,
// форматы с нативной поддержкой) или document (крупные/heic/heif).
// ok=false — файл больше лимита документа (50 МБ), отправлять нечего.
func photoKind(p domain.ComplaintTGPhoto) (kind string, ok bool) {
	if len(p.Data) > telegramDocMaxBytes {
		return "", false
	}
	switch p.Ext {
	case "jpg", "jpeg", "png", "webp", "gif":
		if len(p.Data) <= telegramPhotoMaxBytes {
			return "photo", true
		}
	}
	return "document", true
}

func (n *Notifier) sendMessage(ctx context.Context, chatID int64, text string) error {
	return n.postJSON(ctx, "sendMessage", map[string]any{
		"chat_id": chatID,
		"text":    text,
	})
}

// postJSON выполняет POST-запрос к методу Bot API с JSON-телом.
func (n *Notifier) postJSON(ctx context.Context, method string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal telegram message: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/%s", n.apiBaseURL, n.botToken, method)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create telegram request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram request failed: %w", err)
	}

	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Error(fmt.Sprintf("failed to close telegram response body: %v", err))
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodySnippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("telegram API returned %s: %s", resp.Status, string(bodySnippet))
	}

	return nil
}
