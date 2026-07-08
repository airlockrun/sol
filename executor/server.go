package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/bus"
	"github.com/coder/websocket"
)

// maxWSMessageBytes is the per-message read limit for the toolserver socket.
// coder/websocket defaults to 32 KiB, far too small for tool payloads (a file
// read or a command's output routinely exceeds it). Tools already cap their
// own output, so a generous ceiling here only guards against a runaway frame.
const maxWSMessageBytes = 100 << 20 // 100 MiB

const (
	wsPingInterval = 30 * time.Second
	wsWriteTimeout = 10 * time.Second
)

// ToolServer wraps a tool.Executor and serves requests over WebSocket.
// It runs in the container and executes tools locally.
// It owns a Bus, PermissionManager, and QuestionManager so that tools
// executing inside the container can call AskPermission/AskQuestion.
type ToolServer struct {
	executor    tool.Executor
	mu          sync.RWMutex
	bus         *bus.Bus
	pm          *bus.PermissionManager
	qm          *bus.QuestionManager
	activeTools map[string]struct{} // if non-nil, only these tools are exposed
}

// NewToolServer creates a ToolServer with the given executor.
func NewToolServer(executor tool.Executor) *ToolServer {
	b := bus.New()
	return &ToolServer{
		executor: executor,
		bus:      b,
		pm:       bus.NewPermissionManager(b),
		qm:       bus.NewQuestionManager(b),
	}
}

// PermissionManager returns the server's permission manager.
func (s *ToolServer) PermissionManager() *bus.PermissionManager { return s.pm }

// QuestionManager returns the server's question manager.
func (s *ToolServer) QuestionManager() *bus.QuestionManager { return s.qm }

// message is the wire format for request/response.
type message struct {
	Type     string         `json:"type"`               // "request", "response", "tools", "set_rules", "set_active_tools", "push_answers"
	ID       string         `json:"id,omitempty"`       // Request ID for correlation
	Request  *tool.Request  `json:"request,omitempty"`  // Tool request
	Response *tool.Response `json:"response,omitempty"` // Tool response
	Tools    []tool.Info    `json:"tools,omitempty"`    // Tool definitions
	Error    string         `json:"error,omitempty"`    // Error message

	// Structured fatal errors (server → client)
	PermissionNeeded *bus.ErrPermissionNeeded `json:"permission_needed,omitempty"`
	QuestionNeeded   *bus.ErrQuestionNeeded   `json:"question_needed,omitempty"`

	// Control payloads (client → server)
	Rules       []bus.PermissionRule `json:"rules,omitempty"`
	Answers     [][]string           `json:"answers,omitempty"`
	ActiveTools []string             `json:"active_tools,omitempty"` // tool names to expose
}

// serverConn wraps a connection with a write mutex. coder/websocket forbids
// concurrent data writes; the read loop and the keepalive goroutine both
// touch the socket, so every JSON write goes through writeMsg under the lock.
// (Ping is safe to call concurrently with Write per coder/websocket, so it
// stays outside the lock.)
type serverConn struct {
	ws      *websocket.Conn
	writeMu sync.Mutex
}

func (c *serverConn) writeMsg(ctx context.Context, msg message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return c.ws.Write(wctx, websocket.MessageText, data)
}

// Handler returns an http.Handler for the WebSocket endpoint.
func (s *ToolServer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true, // toolserver runs inside the trusted build container
		})
		if err != nil {
			return
		}
		ws.SetReadLimit(maxWSMessageBytes)
		s.handleConnection(ws)
	})
}

// handleConnection handles a single WebSocket connection. A keepalive ping
// detects a dead peer; any read error (including a failed ping that cancels
// the context) ends the loop and closes the socket — there is no reconnect.
func (s *ToolServer) handleConnection(ws *websocket.Conn) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer ws.Close(websocket.StatusNormalClosure, "")

	sc := &serverConn{ws: ws}
	go s.pingLoop(ctx, ws, cancel)

	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			if !isNormalWSClose(err) {
				log.Printf("build tool runtime connection read failed: %v", err)
			}
			return // connection closed or error
		}
		var msg message
		if err := json.Unmarshal(data, &msg); err != nil {
			s.sendError(ctx, sc, "", fmt.Sprintf("invalid message: %v", err))
			continue
		}
		s.dispatch(ctx, sc, msg)
	}
}

// pingLoop sends periodic pings; a ping that fails (no pong) means the peer is
// gone, so it cancels the connection context to unblock the read loop.
func (s *ToolServer) pingLoop(ctx context.Context, ws *websocket.Conn, cancel context.CancelFunc) {
	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pctx, c := context.WithTimeout(ctx, wsWriteTimeout)
			err := ws.Ping(pctx)
			c()
			if err != nil {
				log.Printf("build tool runtime connection ping failed: %v", err)
				cancel()
				return
			}
		}
	}
}

// dispatch routes one message. A defensive recover keeps a panic in the
// marshal/dispatch layer (tool panics are already recovered in the executor)
// from tearing the connection — it becomes an error reply instead.
func (s *ToolServer) dispatch(ctx context.Context, sc *serverConn, msg message) {
	defer func() {
		if r := recover(); r != nil {
			s.sendError(ctx, sc, msg.ID, fmt.Sprintf("internal error: %v", r))
		}
	}()

	switch msg.Type {
	case "request":
		s.handleRequest(ctx, sc, msg)
	case "tools":
		s.handleToolsRequest(ctx, sc, msg)
	case "set_rules":
		s.handleSetRules(ctx, sc, msg)
	case "set_active_tools":
		s.handleSetActiveTools(ctx, sc, msg)
	case "push_answers":
		s.handlePushAnswers(ctx, sc, msg)
	default:
		s.sendError(ctx, sc, msg.ID, fmt.Sprintf("unknown message type: %s", msg.Type))
	}
}

// handleRequest processes a tool execution request.
func (s *ToolServer) handleRequest(ctx context.Context, sc *serverConn, msg message) {
	if msg.Request == nil {
		s.sendError(ctx, sc, msg.ID, "missing request")
		return
	}

	s.mu.RLock()
	executor := s.executor
	active := s.activeTools
	s.mu.RUnlock()

	// Reject tools not in the active set
	if active != nil {
		if _, ok := active[msg.Request.ToolName]; !ok {
			s.sendError(ctx, sc, msg.ID, fmt.Sprintf("tool %q not available", msg.Request.ToolName))
			return
		}
	}

	// Inject Bus/PM/QM into context so tools can call AskPermission/AskQuestion
	execCtx := context.Background()
	execCtx = bus.WithBus(execCtx, s.bus)
	execCtx = bus.WithPermissionManager(execCtx, s.pm)
	execCtx = bus.WithQuestionManager(execCtx, s.qm)

	resp, err := executor.Execute(execCtx, *msg.Request)
	if err != nil {
		// Check for structured fatal errors that need to propagate
		var permErr *bus.ErrPermissionNeeded
		var questErr *bus.ErrQuestionNeeded
		if errors.As(err, &permErr) {
			s.writeMsg(ctx, sc, message{Type: "response", ID: msg.ID, PermissionNeeded: permErr})
			return
		}
		if errors.As(err, &questErr) {
			s.writeMsg(ctx, sc, message{Type: "response", ID: msg.ID, QuestionNeeded: questErr})
			return
		}
		s.sendError(ctx, sc, msg.ID, err.Error())
		return
	}

	s.writeMsg(ctx, sc, message{Type: "response", ID: msg.ID, Response: &resp})
}

// handleToolsRequest returns the available tool definitions.
func (s *ToolServer) handleToolsRequest(ctx context.Context, sc *serverConn, msg message) {
	s.mu.RLock()
	allTools := s.executor.Tools()
	active := s.activeTools
	s.mu.RUnlock()

	// Filter to active tools if set
	var filtered []tool.Info
	if active != nil {
		for _, t := range allTools {
			if _, ok := active[t.Name]; ok {
				filtered = append(filtered, t)
			}
		}
	} else {
		filtered = allTools
	}

	s.writeMsg(ctx, sc, message{Type: "tools", ID: msg.ID, Tools: filtered})
}

// handleSetRules sets the permission rules and sends an ack.
func (s *ToolServer) handleSetRules(ctx context.Context, sc *serverConn, msg message) {
	s.pm.SetRules(msg.Rules)
	s.writeMsg(ctx, sc, message{Type: "set_rules", ID: msg.ID})
}

// handleSetActiveTools restricts which tools the server exposes and accepts.
func (s *ToolServer) handleSetActiveTools(ctx context.Context, sc *serverConn, msg message) {
	active := make(map[string]struct{}, len(msg.ActiveTools))
	for _, name := range msg.ActiveTools {
		active[name] = struct{}{}
	}
	s.mu.Lock()
	s.activeTools = active
	s.mu.Unlock()
	s.writeMsg(ctx, sc, message{Type: "set_active_tools", ID: msg.ID})
}

// handlePushAnswers pushes pre-loaded answers and sends an ack.
func (s *ToolServer) handlePushAnswers(ctx context.Context, sc *serverConn, msg message) {
	s.qm.PushAnswers(msg.Answers)
	s.writeMsg(ctx, sc, message{Type: "push_answers", ID: msg.ID})
}

// sendError sends an error response.
func (s *ToolServer) sendError(ctx context.Context, sc *serverConn, id, errMsg string) {
	s.writeMsg(ctx, sc, message{Type: "response", ID: id, Error: errMsg})
}

func (s *ToolServer) writeMsg(ctx context.Context, sc *serverConn, msg message) {
	if err := sc.writeMsg(ctx, msg); err != nil && !isNormalWSClose(err) {
		log.Printf("build tool runtime connection write failed: %v", err)
	}
}

// ListenAndServe starts the server on the given address.
func (s *ToolServer) ListenAndServe(addr string) error {
	mux := http.NewServeMux()
	mux.Handle("/ws", s.Handler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	return http.ListenAndServe(addr, mux)
}

// ServeHTTP implements http.Handler for easy integration.
func (s *ToolServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Handler().ServeHTTP(w, r)
}

// MarshalRequest serializes a tool request to JSON.
func MarshalRequest(req tool.Request) ([]byte, error) {
	return json.Marshal(req)
}

// UnmarshalResponse deserializes a tool response from JSON.
func UnmarshalResponse(data []byte) (tool.Response, error) {
	var resp tool.Response
	err := json.Unmarshal(data, &resp)
	return resp, err
}
