// Пакет ws — вебсокет-хаб модуля «Сроки».
//
// Один хаб на обе страницы («Сроки» и «Шорт-лист»): при подключении клиент
// получает полный снапшот каталога с остатками (товары без остатков —
// пустой lots, строка с пустыми клетками), дальше — дельты (lot_upsert).
// Фильтр short_list и пересчёт ширины таблицы — на клиенте.
package ws

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"fmt"
	"log/slog"
	"warehouseHelper/internal/stock"
)

const sendBuffer = 64

// Периоды keep-alive: сервер пингует клиента каждые 30 секунд; если понг не
// пришёл за 60 секунд (клиент упал, сеть оборвалась, ПК спал), соединение
// закрывается — браузер получает onclose и переподключается. Без этого
// мёртвые соединения висят до первой записи или таймаута TCP.
const (
	pingPeriod = 30 * time.Second
	pongWait   = 60 * time.Second
)

// Message — фрейм протокола. При подключении Type = "snapshot" (Rows),
// далее дельты: "lot_upsert" (ProductID + Lot), "lot_delete"
// (ProductID + BestBefore).
type Message struct {
	Type       string          `json:"type"`
	Rows       []stock.Product `json:"rows,omitempty"`        // snapshot
	ProductID  string          `json:"product_id,omitempty"`  // дельты
	BestBefore string          `json:"best_before,omitempty"` // lot_delete: YYYY-MM-DD
	Lot        *stock.Lot      `json:"lot,omitempty"`         // lot_upsert
}

// Hub держит подключённых клиентов и рассылает изменения.
// Реализует usecase.Publisher — DI связывает его с usecase сроков.
type Hub struct {
	upgrader websocket.Upgrader
	mu       sync.Mutex
	clients  map[*Client]struct{}
}

func NewHub() *Hub {
	return &Hub{
		upgrader: websocket.Upgrader{
			// ПК склада ходят по IP и разным портам — origin не проверяем.
			CheckOrigin: func(*http.Request) bool { return true },
		},
		clients: make(map[*Client]struct{}),
	}
}

// Client — одно вебсокет-соединение. Писать в conn можно только из одной
// горутины (gorilla), поэтому все отправки идут через буферизованный send.
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

func NewClient(hub *Hub, conn *websocket.Conn) *Client {
	return &Client{hub: hub, conn: conn, send: make(chan []byte, sendBuffer)}
}

// Register добавляет клиента и сразу ставит в очередь снапшот — под тем же
// мутексом, что и broadcast, так что дельты не могут прийти раньше снапшота.
func (h *Hub) Register(c *Client, snapshot []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = struct{}{}
	c.send <- snapshot
}

// Unregister убирает клиента и закрывает его очередь (writer завершится).
func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
}

// PublishStockChange — реализация usecase.Publisher: рассылает дельту.
func (h *Hub) PublishStockChange(e stock.Event) {
	m := Message{Type: e.Kind, ProductID: e.ProductID, Lot: e.Lot}
	if e.Kind == stock.EventLotDelete {
		m.BestBefore = e.BestBefore.Format(time.DateOnly)
	}
	msg, err := json.Marshal(m)
	if err != nil {
		slog.Error(fmt.Sprintf("ws: marshal event %s: %v", e.Kind, err))
		return
	}
	h.broadcast(msg)
}

// PublishCatalogSnapshot — реализация usecase.Publisher: рассылает клиентам
// полный снапшот каталога с остатками (шов goods: каталог перечитан после
// выгрузки/правки товаров — открытые страницы обновляются без рестарта).
func (h *Hub) PublishCatalogSnapshot(rows []stock.Product) {
	msg, err := json.Marshal(Message{Type: "snapshot", Rows: rows})
	if err != nil {
		slog.Error(fmt.Sprintf("ws: marshal catalog snapshot: %v", err))
		return
	}
	h.broadcast(msg)
}

// ServeConn обслуживает одно соединение: регистрирует клиента со снапшотом
// и запускает read/write-горутины.
func (h *Hub) ServeConn(w http.ResponseWriter, r *http.Request, snapshot []byte) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Клиент получает 500 — это сбой, а не «всё по плану»: уровень ERROR.
		slog.Error(fmt.Sprintf("ws: upgrade: %v", err))
		return
	}
	c := NewClient(h, conn)
	h.Register(c, snapshot)
	go c.writePump()
	go c.readPump()
}

// broadcast рассылает сообщение всем клиентам. Медленный клиент (полный
// буфер) сообщение теряет, но соединение не рвётся.
func (h *Hub) broadcast(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- msg:
		default:
			// Медленный клиент теряет дельту (соединение живёт) — но это
			// потеря данных для клиента: уровень ERROR.
			slog.Error(fmt.Sprintf("ws: send buffer full, dropping message for %s", c.conn.RemoteAddr()))
		}
	}
}

// readPump читает (и игнорирует) сообщения клиента: держит соединение живым
// (ping/pong с таймаутом понга), завершается при разрыве и снимает клиента
// с хаба. Клиент не шлёт данных по ws — все записи идут POST /ms/dates/discount.
func (c *Client) readPump() {
	defer func() {
		c.hub.Unregister(c)
		_ = c.conn.Close()
	}()
	c.conn.SetReadLimit(1024)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

// writePump пишет очередь в соединение и пингует клиента (keep-alive).
// Мёртвое соединение закрывается по таймауту понга в readPump — клиент
// получает onclose и переподключается (см. scheduleReconnect на странице).
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
