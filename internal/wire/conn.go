package wire

import (
	"encoding/json"
	"errors"
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

// CloseWith ends the connection with a close frame that carries code, so the other side reads a fixed code instead of
// a connection that just ends. The frame is best effort: the connection is closed either way.
func (c *Conn) CloseWith(code int) error {
	err := c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, ""),
		time.Now().Add(writeTimeout))
	if cerr := c.ws.Close(); err == nil {
		err = cerr
	}
	return err
}

// CloseCode is the code of the close frame the other side ended the connection with, as Receive returned it: 1006
// (abnormal closure, a code no peer sends) for a connection that just ended, 0 for an error that is no close at all
// (a timeout). gorilla/websocket hands a close frame to the reader only as the error of the read; its code is a field
// of that error, not a text.
func CloseCode(err error) int {
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return 0
}
