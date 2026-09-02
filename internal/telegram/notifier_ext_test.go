package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"warehouseHelper/internal/config"
	"warehouseHelper/internal/domain"
)

func TestNotifyCommonStatusSendsHTMLWithButton(t *testing.T) {
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

	n := testNotifier(&config.TelegramConfig{
		BotToken:     "test-token",
		CommonChatID: -1001234567890,
	}, srv.URL)

	err := n.NotifyCommonStatus(context.Background(),
		`<a href="http://warehouse.local:8080/complaint?id=7">Обращение 7</a>: Ожидаем согласования`,
		"complaint_details:7")
	if err != nil {
		t.Fatalf("NotifyCommonStatus error: %v", err)
	}

	if gotPath != "/bottest-token/sendMessage" {
		t.Errorf("path = %q, want /bottest-token/sendMessage", gotPath)
	}
	if gotBody["parse_mode"] != "HTML" {
		t.Errorf("parse_mode = %v, want HTML", gotBody["parse_mode"])
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
	btn := buttons[0].(map[string]any)
	if btn["text"] != "Получить подробности" {
		t.Errorf("button text = %v, want «Получить подробности»", btn["text"])
	}
	if btn["callback_data"] != "complaint_details:7" {
		t.Errorf("callback_data = %v, want complaint_details:7", btn["callback_data"])
	}
}

func TestNotifyCommonStatusNoOpWithoutChat(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testNotifier(&config.TelegramConfig{BotToken: "test-token"}, srv.URL)
	if err := n.NotifyCommonStatus(context.Background(), "text", "data"); err != nil {
		t.Fatalf("NotifyCommonStatus error: %v", err)
	}
	if hit {
		t.Error("no HTTP request expected without common chat id")
	}
}

func TestAnswerCallback(t *testing.T) {
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
	if err := n.AnswerCallback(context.Background(), "callback-id-42"); err != nil {
		t.Fatalf("AnswerCallback error: %v", err)
	}

	if gotPath != "/bottest-token/answerCallbackQuery" {
		t.Errorf("path = %q, want /bottest-token/answerCallbackQuery", gotPath)
	}
	if gotBody["callback_query_id"] != "callback-id-42" {
		t.Errorf("callback_query_id = %v, want callback-id-42", gotBody["callback_query_id"])
	}
}

func TestSendPhotosSendsMultipartMediaGroup(t *testing.T) {
	var (
		mu      sync.Mutex
		gotPath string
		media   []map[string]string
		files   []string
		fields  = map[string]string{}
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotPath = r.URL.Path

		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if mf := r.MultipartForm; mf != nil {
			if err := json.Unmarshal([]byte(mf.Value["media"][0]), &media); err != nil {
				t.Errorf("media JSON: %v", err)
			}
			for name := range mf.File {
				files = append(files, name)
			}
			for k, v := range mf.Value {
				fields[k] = strings.Join(v, ",")
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testNotifier(&config.TelegramConfig{BotToken: "test-token"}, srv.URL)

	photos := []domain.ComplaintTGPhoto{
		{Ext: "jpg", Data: []byte("jpeg-bytes-1")},
		{Ext: "png", Data: []byte("png-bytes-2")},
	}
	if err := n.SendPhotos(context.Background(), -1001234567890, photos); err != nil {
		t.Fatalf("SendPhotos error: %v", err)
	}

	if gotPath != "/bottest-token/sendMediaGroup" {
		t.Errorf("path = %q, want /bottest-token/sendMediaGroup", gotPath)
	}
	if fields["chat_id"] != "-1001234567890" {
		t.Errorf("chat_id field = %q", fields["chat_id"])
	}
	if len(media) != 2 {
		t.Fatalf("media items = %d, want 2", len(media))
	}
	if media[0]["type"] != "photo" || media[0]["media"] != "attach://file0" {
		t.Errorf("media[0] = %#v, want photo attach://file0", media[0])
	}
	if len(files) != 2 {
		t.Errorf("file parts = %v, want 2", files)
	}
}

func TestSendPhotosSplitsByTen(t *testing.T) {
	var (
		mu    sync.Mutex
		calls int
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var media []map[string]string
		if err := json.Unmarshal([]byte(r.MultipartForm.Value["media"][0]), &media); err != nil {
			t.Errorf("media JSON: %v", err)
		}
		if len(media) > 10 {
			t.Errorf("media group has %d items, want <= 10", len(media))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testNotifier(&config.TelegramConfig{BotToken: "test-token"}, srv.URL)

	photos := make([]domain.ComplaintTGPhoto, 25)
	for i := range photos {
		photos[i] = domain.ComplaintTGPhoto{Ext: "jpg", Data: []byte("x")}
	}
	if err := n.SendPhotos(context.Background(), -1001234567890, photos); err != nil {
		t.Fatalf("SendPhotos error: %v", err)
	}

	if calls != 3 {
		t.Errorf("media group calls = %d, want 3 (10+10+5)", calls)
	}
}

func TestSendPhotosEmptyIsNoOp(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testNotifier(&config.TelegramConfig{BotToken: "test-token"}, srv.URL)
	if err := n.SendPhotos(context.Background(), -1001234567890, nil); err != nil {
		t.Fatalf("SendPhotos error: %v", err)
	}
	if hit {
		t.Error("no HTTP request expected for empty photos")
	}
}

func TestPhotoKind(t *testing.T) {
	tests := []struct {
		name   string
		photo  domain.ComplaintTGPhoto
		want   string
		wantOK bool
	}{
		{"small jpg", domain.ComplaintTGPhoto{Ext: "jpg", Data: make([]byte, 1024)}, "photo", true},
		{"big jpg is document", domain.ComplaintTGPhoto{Ext: "jpg", Data: make([]byte, 11<<20)}, "document", true},
		{"heic is document", domain.ComplaintTGPhoto{Ext: "heic", Data: make([]byte, 1024)}, "document", true},
		{"huge is dropped", domain.ComplaintTGPhoto{Ext: "jpg", Data: make([]byte, 51<<20)}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, ok := photoKind(tt.photo)
			if kind != tt.want || ok != tt.wantOK {
				t.Errorf("photoKind() = (%q, %v), want (%q, %v)", kind, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// TestPollerDispatchesCallbackQuery: первый getUpdates возвращает апдейт с
// callback_query, поллер передаёт его обработчику и сдвигает offset;
// обработчик отменяет контекст — Run завершается.
func TestPollerDispatchesCallbackQuery(t *testing.T) {
	var (
		mu      sync.Mutex
		offsets []string
		body    = `{"ok":true,"result":[{"update_id":11,"callback_query":{"id":"cb-1","from":{"id":1},"message":{"chat":{"id":-1005}},"data":"complaint_details:7"}}]}`
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		offsets = append(offsets, r.URL.Query().Get("offset"))
		if len(offsets) == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer srv.Close()

	var (
		got   CallbackQuery
		calls int
	)
	p := NewPoller("test-token", func(_ context.Context, cb CallbackQuery) error {
		mu.Lock()
		calls++
		got = cb
		mu.Unlock()
		return nil
	})
	p.apiBaseURL = srv.URL

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = p.Run(ctx)
		close(done)
	}()

	// ждём, пока поллер обработает апдейт, затем останавливаем его
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		processed := calls >= 1
		mu.Unlock()
		if processed {
			cancel()
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("поллер не обработал callback за 5 секунд")
		}
		time.Sleep(10 * time.Millisecond)
	}
	<-done

	if got.ID != "cb-1" || got.ChatID != -1005 || got.Data != "complaint_details:7" {
		t.Errorf("callback = %#v, want cb-1 / -1005 / complaint_details:7", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(offsets) < 2 || offsets[1] != "12" {
		t.Errorf("offsets = %v, want second call with offset=12", offsets)
	}
}
