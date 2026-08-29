package ws

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"warehouseHelper/internal/stock"
)

func i16(v int16) *int16 { return &v }

// TestHubSnapshotThenDelta — клиент получает снапшот при подключении,
// затем дельту после PublishStockChange (порядок гарантирован регистрацией
// под мутексом: дельта не может прийти раньше снапшота).
func TestHubSnapshotThenDelta(t *testing.T) {
	hub := NewHub()
	snapshot, err := json.Marshal(Message{
		Type: "snapshot",
		Rows: []stock.Product{{ID: "p1", Name: "Хлеб", GroupName: "Хлебобулочные"}},
	})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.ServeConn(w, r, snapshot)
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	// 1) Снапшот.
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var m Message
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if m.Type != "snapshot" || len(m.Rows) != 1 || m.Rows[0].ID != "p1" {
		t.Errorf("snapshot: %+v", m)
	}

	// 2) Дельта.
	hub.PublishStockChange(stock.Event{
		Kind:      stock.EventLotUpsert,
		ProductID: "p1",
		Lot:       &stock.Lot{BestBefore: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Qty: 3, GeneralManual: i16(7)},
	})
	_, data, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("read delta: %v", err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal delta: %v", err)
	}
	if m.Type != stock.EventLotUpsert || m.ProductID != "p1" || m.Lot == nil || m.Lot.Qty != 3 {
		t.Errorf("delta: %+v", m)
	}
	if m.Lot.GeneralManual == nil || *m.Lot.GeneralManual != 7 {
		t.Errorf("manual скидка в дельте: %v", m.Lot.GeneralManual)
	}
}

// TestHubUnregister — закрытие соединения клиентом не роняет хаб.
func TestHubUnregister(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.ServeConn(w, r, []byte(`{"type":"snapshot","rows":[]}`))
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	conn.Close() // клиент ушёл

	time.Sleep(50 * time.Millisecond)
	hub.PublishStockChange(stock.Event{Kind: stock.EventLotUpsert, ProductID: "p1"})
	// Не паникует — уже хорошо; broadcast идёт в пустой список клиентов.
}
