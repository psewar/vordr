package wire

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// CloseWith reaches the other side as a fixed code it reads with CloseCode; a connection that just ends reads as
// abnormal closure, never as a refusal.
func TestCloseWithReachesTheOtherSide(t *testing.T) {
	codes := make(chan int, 2)
	up := websocket.Upgrader{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn := NewConn(ws)
		defer conn.Close()
		_, _, err = conn.Receive(5 * time.Second)
		codes <- CloseCode(err)
	}))
	t.Cleanup(ts.Close)
	for name, tc := range map[string]struct {
		end  func(*Conn) error
		want int
	}{
		"close code": {func(c *Conn) error { return c.CloseWith(CloseHubKeyMismatch) }, CloseHubKeyMismatch},
		"just ends":  {(*Conn).Close, websocket.CloseAbnormalClosure},
	} {
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = tc.end(NewConn(ws))
		select {
		case got := <-codes:
			if got != tc.want {
				t.Errorf("%s: the other side read close code %d, want %d", name, got, tc.want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the other side read nothing", name)
		}
	}
}
