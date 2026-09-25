package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"warehouseHelper/internal/config"
	"warehouseHelper/internal/domain"
)

// captureServer — тестовый Bot API: копит путь и тело последнего запроса,
// отвечает заданным телом.
type captureServer struct {
	path string
	body map[string]any
	rcvd int
	resp string
}

func newCaptureServer(t *testing.T, resp string) (*captureServer, *httptest.Server) {
	t.Helper()
	srv := &captureServer{resp: resp}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.path = r.URL.Path
		srv.rcvd++
		if err := json.NewDecoder(r.Body).Decode(&srv.body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		if srv.resp != "" {
			if _, err := w.Write([]byte(srv.resp)); err != nil {
				t.Errorf("write response: %v", err)
			}
		}
	}))

	return srv, ts
}

// markupRows — inline_keyboard из тела запроса как строки кнопок.
func markupRows(t *testing.T, body map[string]any) [][]map[string]any {
	t.Helper()
	markup, ok := body[fieldReplyMarkup].(map[string]any)
	if !ok {
		t.Fatalf("reply_markup = %#v, want объект", body[fieldReplyMarkup])
	}
	rows, ok := markup[fieldInlineKbd].([]any)
	if !ok {
		t.Fatalf("inline_keyboard = %#v, want массив строк", markup[fieldInlineKbd])
	}
	out := make([][]map[string]any, 0, len(rows))
	for _, row := range rows {
		buttons, ok := row.([]any)
		if !ok {
			t.Fatalf("строка клавиатуры = %#v, want массив кнопок", row)
		}
		line := make([]map[string]any, 0, len(buttons))
		for _, b := range buttons {
			btn, ok := b.(map[string]any)
			if !ok {
				t.Fatalf("кнопка = %#v, want объект", b)
			}
			line = append(line, btn)
		}
		out = append(out, line)
	}

	return out
}

// SendTask: сообщение-задача уходит в общий канал с одной кнопкой отметки,
// message_id возвращается наружу (модуль задач по нулю понимает «канала нет»).
func TestSendTaskSendsButtonAndReturnsMessageID(t *testing.T) {
	srv, ts := newCaptureServer(t, `{"ok":true,"result":{"message_id":555}}`)
	defer ts.Close()
	n := testNotifier(&config.TelegramConfig{BotToken: "test-token", CommonChatID: -1005}, ts.URL)

	id, err := n.SendTask(context.Background(), "Убрать с сайта: Творог", "✅ Отметить", "task_done:7")
	if err != nil {
		t.Fatalf("SendTask: %v", err)
	}
	if id != 555 {
		t.Errorf("message_id = %d, want 555", id)
	}
	if srv.path != tgSendMessagePath {
		t.Errorf("path = %q, want %s", srv.path, tgSendMessagePath)
	}
	if srv.body[fieldChatID] != float64(-1005) {
		t.Errorf("chat_id = %v, want общий канал", srv.body[fieldChatID])
	}
	if srv.body[fieldText] != "Убрать с сайта: Творог" {
		t.Errorf("text = %v", srv.body[fieldText])
	}
	if _, has := srv.body["parse_mode"]; has {
		t.Errorf("текст задачи ушёл с разметкой: %v", srv.body["parse_mode"])
	}
	rows := markupRows(t, srv.body)
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("клавиатура %#v, want одна кнопка", rows)
	}
	if rows[0][0][fieldText] != "✅ Отметить" || rows[0][0][fieldCallbackData] != "task_done:7" {
		t.Errorf("кнопка %#v, want «✅ Отметить» с данными task_done:7", rows[0][0])
	}
}

// Канал не подключён (нет chat_id) — запроса нет, наружу 0 без ошибки.
func TestSendTaskNoChannel(t *testing.T) {
	srv, ts := newCaptureServer(t, "")
	defer ts.Close()
	n := testNotifier(&config.TelegramConfig{BotToken: "test-token"}, ts.URL)

	id, err := n.SendTask(context.Background(), "Убрать с сайта: Творог", "✅ Отметить", "task_done:7")
	if err != nil || id != 0 {
		t.Fatalf("SendTask без канала = (%d, %v), want (0, nil)", id, err)
	}
	if srv.rcvd != 0 {
		t.Errorf("запросов к Bot API: %d, want 0", srv.rcvd)
	}
}

// EditKeyboard: кнопки того же сообщения меняются на список сотрудников.
func TestEditKeyboardReplacesButtons(t *testing.T) {
	srv, ts := newCaptureServer(t, `{"ok":true,"result":{}}`)
	defer ts.Close()
	n := testNotifier(&config.TelegramConfig{BotToken: "test-token"}, ts.URL)

	err := n.EditKeyboard(context.Background(), -1005, 777, [][]domain.TGButton{
		{{Text: "Иванов И.И.", CallbackData: "task_who:7:1"}, {Text: "Петров П.П.", CallbackData: "task_who:7:2"}},
	})
	if err != nil {
		t.Fatalf("EditKeyboard: %v", err)
	}
	if srv.path != "/bottest-token/editMessageReplyMarkup" {
		t.Errorf("path = %q, want editMessageReplyMarkup", srv.path)
	}
	if srv.body[fieldMessageID] != float64(777) || srv.body[fieldChatID] != float64(-1005) {
		t.Errorf("правка ушла не в то сообщение: %#v", srv.body)
	}
	rows := markupRows(t, srv.body)
	if len(rows) != 1 || len(rows[0]) != 2 || rows[0][1][fieldCallbackData] != "task_who:7:2" {
		t.Errorf("клавиатура %#v, want строку из двух кнопок сотрудников", rows)
	}
}

// EditText: текст переписывается отметкой, кнопки снимаются пустой клавиатурой.
func TestEditTextStripsButtons(t *testing.T) {
	srv, ts := newCaptureServer(t, `{"ok":true,"result":{}}`)
	defer ts.Close()
	n := testNotifier(&config.TelegramConfig{BotToken: "test-token"}, ts.URL)

	err := n.EditText(context.Background(), -1005, 777, "Убрать с сайта: Творог\n✅ Выполнено: Иванов И.И., 25.09 12:05")
	if err != nil {
		t.Fatalf("EditText: %v", err)
	}
	if srv.path != "/bottest-token/editMessageText" {
		t.Errorf("path = %q, want editMessageText", srv.path)
	}
	if srv.body[fieldText] != "Убрать с сайта: Творог\n✅ Выполнено: Иванов И.И., 25.09 12:05" {
		t.Errorf("text = %v", srv.body[fieldText])
	}
	rows := markupRows(t, srv.body)
	if len(rows) != 0 {
		t.Errorf("клавиатура %#v, want пустую (кнопка гаснет)", rows)
	}
}

// Без id сообщения правки не уходят: править нечего.
func TestEditsNoOpWithoutMessageID(t *testing.T) {
	srv, ts := newCaptureServer(t, "")
	defer ts.Close()
	n := testNotifier(&config.TelegramConfig{BotToken: "test-token"}, ts.URL)

	if err := n.EditKeyboard(context.Background(), -1005, 0, nil); err != nil {
		t.Fatalf("EditKeyboard без message_id: %v", err)
	}
	if err := n.EditText(context.Background(), -1005, 0, "текст"); err != nil {
		t.Fatalf("EditText без message_id: %v", err)
	}
	if srv.rcvd != 0 {
		t.Errorf("запросов к Bot API: %d, want 0", srv.rcvd)
	}
}

// AnswerCallbackAlert: клиент показывает всплывающее окно с причиной отказа.
func TestAnswerCallbackAlertShowsText(t *testing.T) {
	srv, ts := newCaptureServer(t, `{"ok":true,"result":true}`)
	defer ts.Close()
	n := testNotifier(&config.TelegramConfig{BotToken: "test-token"}, ts.URL)

	err := n.AnswerCallbackAlert(context.Background(), "cb-42", "Уже отмечено: Иванов И.И., 25.09 12:05")
	if err != nil {
		t.Fatalf("AnswerCallbackAlert: %v", err)
	}
	if srv.path != "/bottest-token/answerCallbackQuery" {
		t.Errorf("path = %q, want answerCallbackQuery", srv.path)
	}
	if srv.body["callback_query_id"] != "cb-42" {
		t.Errorf("callback_query_id = %v", srv.body["callback_query_id"])
	}
	showAlert, ok := srv.body["show_alert"].(bool)
	if !ok || !showAlert || srv.body[fieldText] != "Уже отмечено: Иванов И.И., 25.09 12:05" {
		t.Errorf("тело ответа %#v, want текст во всплывающем окне", srv.body)
	}
}

// Поллер отдаёт обработчику message_id сообщения с кнопкой: по нему модуль
// правит кнопки и текст (шаг «кто отметил»).
func TestPollerPassesMessageID(t *testing.T) {
	var got CallbackQuery
	p := NewPoller("test-token", func(_ context.Context, cb CallbackQuery) error {
		got = cb

		return nil
	})
	p.handle(context.Background(), tgUpdate{
		UpdateID: 11,
		CallbackQuery: &tgCallbackQuery{
			ID:   "cb-1",
			Data: "task_done:7",
			Msg:  &tgMsg{Chat: &tgChat{ID: -1005}, MessageID: 777},
		},
	})

	if got.MessageID != 777 {
		t.Errorf("MessageID = %d, want 777", got.MessageID)
	}
	if got.ChatID != -1005 || got.ID != "cb-1" || got.Data != "task_done:7" {
		t.Errorf("нажатие %+v, want чат -1005 и данные task_done:7", got)
	}
}
