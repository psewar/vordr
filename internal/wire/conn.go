package wire

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const writeTimeout = 10 * time.Second

// Conn is one tunnel connection. Send may be called from several goroutines; Receive from one.
type Conn struct {
	ws *websocket.Conn
	mu sync.Mutex
}

func NewConn(ws *websocket.Conn) *Conn {
	ws.SetReadLimit(MaxMessage)
	return &Conn{ws: ws}
}

// Send encodes one message. The caller sets its type field.
func (c *Conn) Send(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ws.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	return c.ws.WriteJSON(v)
}

// Receive waits up to timeout for the next message and returns its type and raw bytes.
func (c *Conn) Receive(timeout time.Duration) (string, []byte, error) {
	if err := c.ws.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return "", nil, err
	}
	_, data, err := c.ws.ReadMessage()
	if err != nil {
		return "", nil, err
	}
	var t Type
	if err := json.Unmarshal(data, &t); err != nil {
		return "", data, err
	}
	return t.T, data, nil
}

func (c *Conn) Close() error { return c.ws.Close() }
