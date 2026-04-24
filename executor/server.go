package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/bus"
	"golang.org/x/net/websocket"
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

// Handler returns an http.Handler for the WebSocket endpoint.
func (s *ToolServer) Handler() http.Handler {
	return websocket.Handler(s.handleConnection)
}

// handleConnection handles a single WebSocket connection.
func (s *ToolServer) handleConnection(ws *websocket.Conn) {
	defer ws.Close()

	for {
		var msg message
		if err := websocket.JSON.Receive(ws, &msg); err != nil {
			// Connection closed or error
			return
		}

		switch msg.Type {
		case "request":
			s.handleRequest(ws, msg)
		case "tools":
			s.handleToolsRequest(ws, msg)
		case "set_rules":
			s.handleSetRules(ws, msg)
		case "set_active_tools":
			s.handleSetActiveTools(ws, msg)
		case "push_answers":
			s.handlePushAnswers(ws, msg)
		default:
			s.sendError(ws, msg.ID, fmt.Sprintf("unknown message type: %s", msg.Type))
		}
	}
}

// handleRequest processes a tool execution request.
func (s *ToolServer) handleRequest(ws *websocket.Conn, msg message) {
	if msg.Request == nil {
		s.sendError(ws, msg.ID, "missing request")
		return
	}

	s.mu.RLock()
	executor := s.executor
	active := s.activeTools
	s.mu.RUnlock()

	// Reject tools not in the active set
	if active != nil {
		if _, ok := active[msg.Request.ToolName]; !ok {
			s.sendError(ws, msg.ID, fmt.Sprintf("tool %q not available", msg.Request.ToolName))
			return
		}
	}

	// Inject Bus/PM/QM into context so tools can call AskPermission/AskQuestion
	ctx := context.Background()
	ctx = bus.WithBus(ctx, s.bus)
	ctx = bus.WithPermissionManager(ctx, s.pm)
	ctx = bus.WithQuestionManager(ctx, s.qm)

	resp, err := executor.Execute(ctx, *msg.Request)
	if err != nil {
		// Check for structured fatal errors that need to propagate
		var permErr *bus.ErrPermissionNeeded
		var questErr *bus.ErrQuestionNeeded
		if errors.As(err, &permErr) {
			websocket.JSON.Send(ws, message{Type: "response", ID: msg.ID, PermissionNeeded: permErr})
			return
		}
		if errors.As(err, &questErr) {
			websocket.JSON.Send(ws, message{Type: "response", ID: msg.ID, QuestionNeeded: questErr})
			return
		}
		s.sendError(ws, msg.ID, err.Error())
		return
	}

	// Send response
	reply := message{
		Type:     "response",
		ID:       msg.ID,
		Response: &resp,
	}
	websocket.JSON.Send(ws, reply)
}

// handleToolsRequest returns the available tool definitions.
func (s *ToolServer) handleToolsRequest(ws *websocket.Conn, msg message) {
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

	reply := message{
		Type:  "tools",
		ID:    msg.ID,
		Tools: filtered,
	}
	websocket.JSON.Send(ws, reply)
}

// handleSetRules sets the permission rules and sends an ack.
func (s *ToolServer) handleSetRules(ws *websocket.Conn, msg message) {
	s.pm.SetRules(msg.Rules)
	websocket.JSON.Send(ws, message{Type: "set_rules", ID: msg.ID})
}

// handleSetActiveTools restricts which tools the server exposes and accepts.
func (s *ToolServer) handleSetActiveTools(ws *websocket.Conn, msg message) {
	active := make(map[string]struct{}, len(msg.ActiveTools))
	for _, name := range msg.ActiveTools {
		active[name] = struct{}{}
	}
	s.mu.Lock()
	s.activeTools = active
	s.mu.Unlock()
	websocket.JSON.Send(ws, message{Type: "set_active_tools", ID: msg.ID})
}

// handlePushAnswers pushes pre-loaded answers and sends an ack.
func (s *ToolServer) handlePushAnswers(ws *websocket.Conn, msg message) {
	s.qm.PushAnswers(msg.Answers)
	websocket.JSON.Send(ws, message{Type: "push_answers", ID: msg.ID})
}

// sendError sends an error response.
func (s *ToolServer) sendError(ws *websocket.Conn, id, errMsg string) {
	reply := message{
		Type:  "response",
		ID:    id,
		Error: errMsg,
	}
	websocket.JSON.Send(ws, reply)
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
