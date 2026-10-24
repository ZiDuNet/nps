package conn

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWebSocketStreamPresentsBinaryFramesAsByteStream(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			messageType, payload, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if messageType != websocket.BinaryMessage {
				return
			}
			if err := ws.WriteMessage(websocket.BinaryMessage, payload); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	endpoint := "ws" + server.URL[len("http"):]
	ws, _, err := websocket.DefaultDialer.Dial(endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := NewWebSocketStream(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(2 * time.Second))

	if _, err := stream.Write([]byte("first-")); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("first-second"))
	if _, err := io.ReadFull(stream, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "first-second" {
		t.Fatalf("stream payload = %q, want %q", got, "first-second")
	}
}
