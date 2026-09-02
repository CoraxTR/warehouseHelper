package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// CallbackQuery — нажатие inline-кнопки, пришедшее из Bot API.
type CallbackQuery struct {
	// ID — идентификатор callback-запроса: на него обязательно отвечают
	// answerCallbackQuery, иначе Telegram показывает «часики» на кнопке.
	ID string
	// ChatID — чат, в котором находится сообщение с кнопкой
	// (message.chat.id) — сюда шлётся ответ на нажатие.
	ChatID int64
	// Data — callback_data кнопки.
	Data string
}

// CallbackHandler обрабатывает одно нажатие кнопки (диспетчеризует по Data).
type CallbackHandler func(ctx context.Context, cb CallbackQuery) error

// Poller принимает апдейты бота через long polling getUpdates. Поллер
// обрабатывает только callback_query (остальные типы апдейтов пропускает),
// чтобы не мешать остальным частям приложения. Работать должен ровно один
// поллер на токен (второй getUpdates тем же токеном получит 409 Conflict),
// и нельзя одновременно использовать webhook.
type Poller struct {
	httpClient *http.Client
	apiBaseURL string
	botToken   string
	handler    CallbackHandler

	offset int64 // последний обработанный update_id + 1 (курсор апдейтов)
}

// NewPoller создаёт поллер для токена бота. handler вызывается на каждое
// нажатие inline-кнопки.
func NewPoller(botToken string, handler CallbackHandler) *Poller {
	return &Poller{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		apiBaseURL: telegramAPIBase,
		botToken:   botToken,
		handler:    handler,
	}
}

// tgUpdate — минимальная структура апдейта getUpdates (остальные поля не нужны).
type tgUpdate struct {
	UpdateID      int64            `json:"update_id"`
	CallbackQuery *tgCallbackQuery `json:"callback_query"`
}

type tgCallbackQuery struct {
	ID   string `json:"id"`
	Data string `json:"data"`
	From tgUser `json:"from"`
	Msg  *tgMsg `json:"message"`
}

type tgUser struct {
	ID int64 `json:"id"`
}

type tgMsg struct {
	Chat *tgChat `json:"chat"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

// tgUpdatesResponse — ответ Bot API на getUpdates.
type tgUpdatesResponse struct {
	OK     bool       `json:"ok"`
	Result []tgUpdate `json:"result"`
}

// Run запускает цикл long polling и блокирует вызывающую горутину:
// каждый цикл запрашивает апдейты (timeout=25 — сервер держит соединение,
// пока не появится апдейт), обрабатывает все callback_query и завершает
// итерацию. Ошибки сети/API логируются, курсор не двигается — апдейты
// будут доставлены повторно. Завершается по отмене ctx.
func (p *Poller) Run(ctx context.Context) error {
	if p.botToken == "" {
		slog.Info("telegram: поллер не запущен: токен бота не настроен")
		return nil
	}

	for {
		updates, err := p.getUpdates(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Info(fmt.Sprintf("telegram: getUpdates: %v", err))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3 * time.Second): // пауза перед ретраем, чтобы не долбить API
			}
			continue
		}

		for _, u := range updates {
			if u.CallbackQuery == nil {
				continue // поллер интересуют только нажатия кнопок
			}
			p.handle(ctx, u)
			p.offset = u.UpdateID + 1
		}
	}
}

// handle обрабатывает одно нажатие кнопки. Ошибка обработчика логируется:
// курсор апдейтов уже сдвинут вызывающим, повторной доставки не будет.
func (p *Poller) handle(ctx context.Context, u tgUpdate) {
	cb := u.CallbackQuery
	if cb.ID == "" || cb.Msg == nil || cb.Msg.Chat == nil {
		slog.Info("telegram: callback_query без id/чата, пропущен")
		return
	}

	query := CallbackQuery{
		ID:     cb.ID,
		ChatID: cb.Msg.Chat.ID,
		Data:   cb.Data,
	}
	if err := p.handler(ctx, query); err != nil {
		slog.Info(fmt.Sprintf("telegram: обработка callback %q: %v", cb.Data, err))
	}
}

// getUpdates делает один long-poll запрос и возвращает апдейты,
// накопленные с последнего обработанного update_id.
func (p *Poller) getUpdates(ctx context.Context) ([]tgUpdate, error) {
	url := fmt.Sprintf("%s/bot%s/getUpdates?timeout=25&offset=%d",
		p.apiBaseURL, p.botToken, p.offset)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Error(fmt.Sprintf("telegram: close getUpdates body: %v", err))
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("telegram API returned %s: %s", resp.Status, string(body))
	}

	var parsed tgUpdatesResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode getUpdates response: %w", err)
	}
	if !parsed.OK {
		return nil, errors.New("getUpdates ok=false")
	}
	return parsed.Result, nil
}
