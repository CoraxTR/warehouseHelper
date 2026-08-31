// Package telegram — отправка уведомлений в Telegram через Bot API.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"log/slog"
	"warehouseHelper/internal/config"
)

const (
	// telegramAPIBase — базовый URL Bot API; переопределяется в тестах.
	telegramAPIBase = "https://api.telegram.org"
	sendTimeout     = 15 * time.Second
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
}

// NewNotifier собирает Notifier из конфигурации Telegram.
func NewNotifier(cfg *config.TelegramConfig) *Notifier {
	return &Notifier{
		httpClient:      &http.Client{Timeout: sendTimeout},
		apiBaseURL:      telegramAPIBase,
		botToken:        cfg.BotToken,
		warehouseChatID: cfg.WarehouseChatID,
		everyoneChatID:  cfg.EveryoneChatID,
	}
}

// NotifyWarehouse отправляет сообщение в чат склада.
// Без настроенного токена или chat_id склада — no-op.
func (n *Notifier) NotifyWarehouse(text string) error {
	if n.botToken == "" || n.warehouseChatID == 0 {
		return nil
	}

	return n.sendMessage(n.warehouseChatID, text)
}

// NotifyEveryone отправляет сообщение в общий чат сотрудников.
// Без настроенного токена или chat_id общего чата — no-op.
func (n *Notifier) NotifyEveryone(text string) error {
	if n.botToken == "" || n.everyoneChatID == 0 {
		return nil
	}

	return n.sendMessage(n.everyoneChatID, text)
}

func (n *Notifier) sendMessage(chatID int64, text string) error {
	payload, err := json.Marshal(map[string]any{
		"chat_id": chatID,
		"text":    text,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal telegram message: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", n.apiBaseURL, n.botToken)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create telegram request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram request failed: %w", err)
	}

	defer func() {
		err = resp.Body.Close()
		if err != nil {
			slog.Error(fmt.Sprintf("failed to close telegram response body: %v", err))
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

		return fmt.Errorf("telegram API returned %s: %s", resp.Status, string(body))
	}

	return nil
}
