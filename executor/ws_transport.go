package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/bus"
	"github.com/coder/websocket"
)

// WSTransport implements Transport using WebSocket.
type WSTransport struct {
	conn    *websocket.Conn
	ctx     context.Context
	cancel  context.CancelFunc
	writeMu sync.Mutex

	pending   map[string]chan message
	pendingMu sync.Mutex
	nextID    atomic.Uint64
	closed    atomic.Bool
	done      chan struct{}
}

// NewWSTransport creates a WebSocket transport by connecting to the given URL.
// The URL should be in the format "ws://host:port/ws".
// Optional headers are sent during the handshake (e.g., Authorization).
func NewWSTransport(url string, headers ...http.Header) (*WSTransport, error) {
	ctx, cancel := context.WithCancel(context.Background())

	opts := &websocket.DialOptions{}
	if len(headers) > 0 {
		opts.HTTPHeader = headers[0]
	}

	conn, _, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	conn.SetReadLimit(maxWSMessageBytes)

	t := &WSTransport{
		conn:    conn,
		ctx:     ctx,
		cancel:  cancel,
		pending: make(map[string]chan message),
		done:    make(chan struct{}),
	}

	go t.readLoop()
	go t.pingLoop()

	return t, nil
}

// Send implements Transport.
func (t *WSTransport) Send(ctx context.Context, req tool.Request) (tool.Response, error) {
	if t.closed.Load() {
		return tool.Response{}, errors.New("transport closed")
	}

	msg := message{
		Type:    "request",
		ID:      t.allocID(),
		Request: &req,
	}

	resp, err := t.roundTrip(ctx, msg)
	if err != nil {
		return tool.Response{}, err
	}

	// Check structured fatal errors before flat error string
	if resp.PermissionNeeded != nil {
		return tool.Response{}, resp.PermissionNeeded
	}
	if resp.QuestionNeeded != nil {
		return tool.Response{}, resp.QuestionNeeded
	}

	if resp.Error != "" {
		return tool.Response{
			Output:  resp.Error,
			IsError: true,
		}, nil
	}
	if resp.Response == nil {
		return tool.Response{}, errors.New("empty response")
	}
	return *resp.Response, nil
}

// FetchTools requests the tool definitions from the remote server.
func (t *WSTransport) FetchTools(ctx context.Context) ([]tool.Info, error) {
	if t.closed.Load() {
		return nil, errors.New("transport closed")
	}

	resp, err := t.roundTrip(ctx, message{Type: "tools", ID: t.allocID()})
	if err != nil {
		return nil, err
	}

	if resp.Error != "" {
		return nil, errors.New(resp.Error)
	}
	return resp.Tools, nil
}

// SetRules sends permission rules to the remote ToolServer and waits for ack.
func (t *WSTransport) SetRules(ctx context.Context, rules []bus.PermissionRule) error {
	if t.closed.Load() {
		return errors.New("transport closed")
	}

	resp, err := t.roundTrip(ctx, message{Type: "set_rules", ID: t.allocID(), Rules: rules})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	return nil
}

// SetActiveTools tells the remote ToolServer which tools to expose and accept.
func (t *WSTransport) SetActiveTools(ctx context.Context, toolNames []string) error {
	if t.closed.Load() {
		return errors.New("transport closed")
	}

	resp, err := t.roundTrip(ctx, message{Type: "set_active_tools", ID: t.allocID(), ActiveTools: toolNames})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	return nil
}

// PushAnswers sends pre-loaded answers to the remote ToolServer and waits for ack.
func (t *WSTransport) PushAnswers(ctx context.Context, answers [][]string) error {
	if t.closed.Load() {
		return errors.New("transport closed")
	}

	resp, err := t.roundTrip(ctx, message{Type: "push_answers", ID: t.allocID(), Answers: answers})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	return nil
}

// allocID generates a unique request ID.
func (t *WSTransport) allocID() string {
	return fmt.Sprintf("%d", t.nextID.Add(1))
}

// roundTrip sends a message and waits for the correlated response.
func (t *WSTransport) roundTrip(ctx context.Context, msg message) (message, error) {
	respCh := make(chan message, 1)
	t.pendingMu.Lock()
	t.pending[msg.ID] = respCh
	t.pendingMu.Unlock()

	defer func() {
		t.pendingMu.Lock()
		delete(t.pending, msg.ID)
		t.pendingMu.Unlock()
	}()

	if err := t.writeMsg(ctx, msg); err != nil {
		return message{}, fmt.Errorf("failed to send: %w", err)
	}

	select {
	case <-ctx.Done():
		return message{}, ctx.Err()
	case <-t.done:
		return message{}, errors.New("transport closed")
	case resp := <-respCh:
		return resp, nil
	}
}

// writeMsg serializes and writes a message under the write lock (coder/websocket
// forbids concurrent data writes).
func (t *WSTransport) writeMsg(ctx context.Context, msg message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return t.conn.Write(wctx, websocket.MessageText, data)
}

// readLoop reads messages from the WebSocket and dispatches responses.
func (t *WSTransport) readLoop() {
	defer close(t.done)
	defer t.cancel()

	for {
		_, data, err := t.conn.Read(t.ctx)
		if err != nil {
			return // connection closed or error
		}
		var msg message
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		// Dispatch response to pending request by ID
		if msg.ID != "" {
			t.pendingMu.Lock()
			if ch, ok := t.pending[msg.ID]; ok {
				select {
				case ch <- msg:
				default:
				}
			}
			t.pendingMu.Unlock()
		}
	}
}

// pingLoop keeps the connection alive and surfaces a dead server: a failed
// ping cancels the context, which unblocks readLoop and closes the transport.
func (t *WSTransport) pingLoop() {
	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-t.ctx.Done():
			return
		case <-t.done:
			return
		case <-ticker.C:
			pctx, cancel := context.WithTimeout(t.ctx, wsWriteTimeout)
			err := t.conn.Ping(pctx)
			cancel()
			if err != nil {
				t.cancel()
				return
			}
		}
	}
}

// Close implements Transport.
func (t *WSTransport) Close() error {
	if t.closed.Swap(true) {
		return nil // Already closed
	}
	t.cancel()
	return t.conn.Close(websocket.StatusNormalClosure, "")
}
