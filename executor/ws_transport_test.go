package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func TestRequireCommandResultsRejectsMissingCapabilityBeforeExecution(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var msg message
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Error(err)
				return
			}
			if msg.Type != "tools" {
				t.Errorf("executed %s before capability validation", msg.Type)
				return
			}
			data, _ = json.Marshal(message{Type: "tools", ID: msg.ID})
			if err := conn.Write(context.Background(), websocket.MessageText, data); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	transport, err := NewWSTransport("ws" + strings.TrimPrefix(server.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	err = transport.RequireCommandResults(t.Context())
	if err == nil || !strings.Contains(err.Error(), "requires rebuild") || !strings.Contains(err.Error(), "no command was dispatched") {
		t.Fatalf("capability error: %v", err)
	}
}
