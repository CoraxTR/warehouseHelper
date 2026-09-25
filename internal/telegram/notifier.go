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

	// Имена полей payload'ов Bot API — повторяются в каждом методе нотифаера
	// (goconst: вынесены в константы, 16.09.2026).
	fieldChatID       = "chat_id"
	fieldText         = "text"
	fieldCommands     = "commands"
	fieldMessageID    = "message_id"
	fieldReplyMarkup  = "reply_markup"
	fieldInlineKbd    = "inline_keyboard"
	fieldCallbackData = "callback_data"
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
		fieldChatID:  n.commonChatID,
		fieldText:    textHTML,
		"parse_mode": "HTML",
		fieldReplyMarkup: map[string]any{
			fieldInlineKbd: [][]map[string]string{{
				{fieldText: "Получить подробности", fieldCallbackData: callbackData},
			}},
		},
	}
	return n.postJSON(ctx, "sendMessage", payload)
}

// SendTask отправляет в общий канал сообщение-задачу модуля «Внутренние
// задачи»: обычный текст (без разметки — имена товаров со спецсимволами) и
// одна inline-кнопка отметки (buttonText + callbackData).
//
// Возвращает message_id отправленного сообщения; 0 — канал не подключён
// (пустой токен или chat_id общего канала) либо API не вернул id. Вызывающий
// (модуль задач) по нулю понимает, что задачи в чате нет, и строку ленты не
// заводит.
func (n *Notifier) SendTask(ctx context.Context, text, buttonText, callbackData string) (int64, error) {
	if n.botToken == "" || n.commonChatID == 0 {
		return 0, nil
	}

	payload := map[string]any{
		fieldChatID: n.commonChatID,
		fieldText:   text,
		fieldReplyMarkup: map[string]any{
			fieldInlineKbd: [][]map[string]string{{
				{fieldText: buttonText, fieldCallbackData: callbackData},
			}},
		},
	}
	return n.postJSONResult(ctx, "sendMessage", payload)
}

// EditKeyboard заменяет inline-кнопки отправленного сообщения (шаг «кто
// отметил»): вопрос виден там же, где задача, отдельного сообщения нет.
// Пустой rows — снять кнопки. Без токена или id сообщения — no-op.
func (n *Notifier) EditKeyboard(ctx context.Context, chatID, messageID int64, rows [][]domain.TGButton) error {
	if n.botToken == "" || chatID == 0 || messageID == 0 {
		return nil
	}

	keyboard := make([][]map[string]string, 0, len(rows))
	for _, row := range rows {
		buttons := make([]map[string]string, 0, len(row))
		for _, b := range row {
			buttons = append(buttons, map[string]string{fieldText: b.Text, fieldCallbackData: b.CallbackData})
		}
		keyboard = append(keyboard, buttons)
	}

	return n.postJSON(ctx, "editMessageReplyMarkup", map[string]any{
		fieldChatID:      chatID,
		fieldMessageID:   messageID,
		fieldReplyMarkup: map[string]any{fieldInlineKbd: keyboard},
	})
}

// EditText заменяет текст отправленного сообщения и СНИМАЕТ его inline-кнопки
// (пустая клавиатура): отмеченная задача больше не нажимается. Текст —
// обычный, без разметки. Без токена или id сообщения — no-op.
func (n *Notifier) EditText(ctx context.Context, chatID, messageID int64, text string) error {
	if n.botToken == "" || chatID == 0 || messageID == 0 {
		return nil
	}

	return n.postJSON(ctx, "editMessageText", map[string]any{
		fieldChatID:      chatID,
		fieldMessageID:   messageID,
		fieldText:        text,
		fieldReplyMarkup: map[string]any{fieldInlineKbd: [][]map[string]string{}},
	})
}

// SendWarehouseReturn отправляет в чат склада сообщение с URL-кнопкой
// «Расформировать» (модуль returns: возврат в продажу) — обёртка над
// SendWarehouseButton с фиксированным текстом кнопки. Возвращает chat_id и
// message_id отправленного сообщения — по ним модуль удалит сообщение после
// обработки возврата. Без токена или chat_id склада — no-op.
func (n *Notifier) SendWarehouseReturn(ctx context.Context, text, buttonURL string) (chatID, messageID int64, err error) {
	return n.SendWarehouseButton(ctx, text, "Расформировать", buttonURL)
}

// SendWarehouseButton отправляет в чат склада сообщение с URL-кнопкой
// (buttonText — текст кнопки: «Расформировать», «Подобрать» и т.п.).
// Текст — обычный, без HTML-разметки (имена товаров могут содержать
// спецсимволы). Возвращает chat_id и message_id отправленного сообщения —
// по ним модуль удалит сообщение после обработки. Без токена или chat_id
// склада — no-op (0, 0, nil).
func (n *Notifier) SendWarehouseButton(ctx context.Context, text, buttonText, buttonURL string) (chatID, messageID int64, err error) {
	if n.botToken == "" || n.warehouseChatID == 0 {
		return 0, 0, nil
	}

	payload := map[string]any{
		fieldChatID: n.warehouseChatID,
		fieldText:   text,
		fieldReplyMarkup: map[string]any{
			fieldInlineKbd: [][]map[string]string{{
				{fieldText: buttonText, "url": buttonURL},
			}},
		},
	}

	messageID, err = n.postJSONResult(ctx, "sendMessage", payload)
	return n.warehouseChatID, messageID, err
}

// DeleteWarehouseMessage удаляет сообщение из чата склада по message_id
// (модуль reservewatch убирает уведомление, когда проблема резерва исчезла).
// Telegram молча игнорирует удаление несуществующего сообщения. Без токена —
// no-op.
func (n *Notifier) DeleteWarehouseMessage(ctx context.Context, messageID int64) error {
	if n.botToken == "" || n.warehouseChatID == 0 || messageID == 0 {
		return nil
	}
	return n.postJSON(ctx, "deleteMessage", map[string]any{
		fieldChatID:    n.warehouseChatID,
		fieldMessageID: messageID,
	})
}

// DeleteMessage удаляет сообщение из чата (модуль returns убирает из чата
// обработанное уведомление). Telegram молча игнорирует удаление
// несуществующего сообщения. Без токена — no-op.
func (n *Notifier) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	if n.botToken == "" || chatID == 0 || messageID == 0 {
		return nil
	}
	return n.postJSON(ctx, "deleteMessage", map[string]any{
		fieldChatID:    chatID,
		fieldMessageID: messageID,
	})
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

// AnswerCallbackAlert закрывает нажатие ВСПЛЫВАЮЩИМ окном с текстом (клиент
// показывает его до подтверждения) — так сообщаются причины отказа: задача
// уже отмечена (видно, кем и когда), задачи нет, база сотрудников пуста.
// Без токена или id нажатия — no-op.
func (n *Notifier) AnswerCallbackAlert(ctx context.Context, callbackQueryID, alert string) error {
	if n.botToken == "" || callbackQueryID == "" {
		return nil
	}
	return n.postJSON(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": callbackQueryID,
		fieldText:           alert,
		"show_alert":        true,
	})
}

// BotCommand — команда бота для меню «/» в клиентах Telegram.
//
// Command — имя БЕЗ слэша: Telegram принимает там только строчные латинские
// буквы, цифры и подчёркивание (до 32 символов). Кириллицу в имени клиент не
// примет: команду не подсветит, в меню не покажет и тапом не наберёт, — поэтому
// русское название команды живёт в Description (свободный текст до 256 символов).
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SetCommands задаёт меню команд бота (setMyCommands) — тот список, который
// клиенты Telegram показывают по нажатию «/».
//
// Вызов ЗАМЕЩАЕТ список целиком, в том числе заданный в BotFather: список
// должен быть полным перечнем команд бота, а не «добавкой» одной команды.
// Без токена или с пустым списком — no-op (меню уже настроенного бота не
// трогаем, чтобы не стереть его пустотой).
func (n *Notifier) SetCommands(ctx context.Context, cmds []BotCommand) error {
	if n.botToken == "" || len(cmds) == 0 {
		return nil
	}

	return n.postJSON(ctx, "setMyCommands", map[string]any{fieldCommands: cmds})
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

	if err := mw.WriteField(fieldChatID, fmt.Sprintf("%d", chatID)); err != nil {
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
		return "document", true
	default:
		return "document", true
	}
}

func (n *Notifier) sendMessage(ctx context.Context, chatID int64, text string) error {
	return n.postJSON(ctx, "sendMessage", map[string]any{
		fieldChatID: chatID,
		fieldText:   text,
	})
}

// postJSON выполняет POST-запрос к методу Bot API с JSON-телом.
func (n *Notifier) postJSON(ctx context.Context, method string, payload map[string]any) error {
	_, err := n.postJSONResult(ctx, method, payload)
	return err
}

// postJSONResult выполняет POST-запрос к методу Bot API и разбирает ответ:
// возвращает message_id из result (0, если метод не возвращает сообщение).
func (n *Notifier) postJSONResult(ctx context.Context, method string, payload map[string]any) (int64, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal telegram message: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/%s", n.apiBaseURL, n.botToken, method)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("failed to create telegram request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("telegram request failed: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Error(fmt.Sprintf("failed to close telegram response body: %v", err))
		}
	}()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return 0, fmt.Errorf("failed to read telegram response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("telegram API returned %s: %s", resp.Status, string(respBody))
	}

	// Пустой 2xx-ответ (часть тестовых серверов) — сообщение без id.
	if len(bytes.TrimSpace(respBody)) == 0 {
		return 0, nil
	}

	var r struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(respBody, &r); err != nil {
		return 0, fmt.Errorf("failed to parse telegram response: %w", err)
	}

	// result — не всегда объект: answerCallbackQuery и deleteMessage отвечают
	// `true`. message_id ищем только в объекте, иначе возвращаем 0 (25.09.2026:
	// прежний безусловный разбор в структуру падал на `true` и логировал
	// ошибку на каждом нажатии кнопки, хотя ответ уже дошёл).
	if len(r.Result) == 0 || r.Result[0] != '{' {
		return 0, nil
	}

	var res struct {
		MessageID int64 `json:"message_id"`
	}
	if err := json.Unmarshal(r.Result, &res); err != nil {
		return 0, fmt.Errorf("failed to parse telegram result: %w", err)
	}
	return res.MessageID, nil
}
