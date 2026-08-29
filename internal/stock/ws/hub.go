// Пакет ws — вебсокет-хаб модуля «Сроки».
//
// Один хаб на обе страницы («Сроки» и «Шорт-лист»): при подключении клиент
// получает полный снапшот остатков, дальше — дельты (lot_upsert).
// Фильтр short_list и пересчёт ширины таблицы — на клиенте.
package ws

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"

	"warehouseHelper/internal/stock"
)

const sendBuffer = 64

// Message — фрейм протокола. При подключении Type = "snapshot" (Rows),
// далее дельты: "lot_upsert" (ProductID + Lot), в будущем —
// "lot_delete" / "product_delete".
type Message struct {
	Type      string          `json:"type"`
	Rows      []stock.Product `json:"rows,omitempty"`       // snapshot
	ProductID string          `json:"product_id,omitempty"` // дельты
	Lot       *stock.Lot      `json:"lot,omitempty"`        // lot_upsert
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
	msg, err := json.Marshal(Message{Type: e.Kind, ProductID: e.ProductID, Lot: e.Lot})
	if err != nil {
		log.Printf("ws: marshal event %s: %v", e.Kind, err)
		return
	}
	h.broadcast(msg)
}

// ServeConn обслуживает одно соединение: регистрирует клиента со снапшотом
// и запускает read/write-горутины.
func (h *Hub) ServeConn(w http.ResponseWriter, r *http.Request, snapshot []byte) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws: upgrade: %v", err)
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
			log.Printf("ws: send buffer full, dropping message for %s", c.conn.RemoteAddr())
		}
	}
}

// readPump читает (и игнорирует) сообщения клиента: держит соединение живым
// (ping/pong), завершается при разрыве и снимает клиента с хаба.
// Клиент не шлёт данных по ws — все записи идут POST /ms/dates/discount.
func (c *Client) readPump() {
	defer func() {
		c.hub.Unregister(c)
		_ = c.conn.Close()
	}()
	c.conn.SetReadLimit(1024)
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

// writePump пишет очередь в соединение.
func (c *Client) writePump() {
	defer func() { _ = c.conn.Close() }()
	for msg := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}
}
