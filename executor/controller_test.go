package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/bus"
	"github.com/airlockrun/sol/tools"
	"github.com/coder/websocket"
)

// The fake is the trusted authority, not a parser of client-provided epochs.
// Tickets are opaque and are valid only for an explicitly registered boot.
type testControllerAuthority struct {
	mu         sync.Mutex
	identity   ControllerIdentity
	grants     map[string]ControllerGrant
	identities map[string]ControllerIdentity
	denied     map[string]bool
	calls      map[string]int
	validate   func(string, int) error
}

func (a *testControllerAuthority) Validate(ctx context.Context, identity ControllerIdentity, credential string) (ControllerGrant, error) {
	if err := ctx.Err(); err != nil {
		return ControllerGrant{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls[credential]++
	grant, ok := a.grants[credential]
	if !ok || a.denied[credential] || identity != a.identity || identity != a.identities[credential] {
		return ControllerGrant{}, errors.New("not a current host admission")
	}
	if a.validate != nil {
		if err := a.validate(credential, a.calls[credential]); err != nil {
			return ControllerGrant{}, err
		}
	}
	return grant, nil
}

func (a *testControllerAuthority) issue(credential string, epoch uint64, deadline time.Time) ControllerGrant {
	a.mu.Lock()
	defer a.mu.Unlock()
	grant := ControllerGrant{Epoch: epoch, AdmissionID: "host-admission-" + credential, ValidUntil: deadline}
	a.grants[credential] = grant
	a.identities[credential] = a.identity
	return grant
}

type controllerTestExecutor struct {
	entered, cancelled, release, finished chan struct{}
	calls                                 atomic.Int64
	blockOnce                             sync.Once
}

func (e *controllerTestExecutor) Tools() []tool.Info {
	return []tool.Info{{Name: "hold"}, {Name: "mark"}}
}
func (e *controllerTestExecutor) Execute(ctx context.Context, req tool.Request) (tool.Response, error) {
	e.calls.Add(1)
	if req.ToolName == "hold" {
		e.blockOnce.Do(func() {
			close(e.entered)
			<-ctx.Done()
			close(e.cancelled)
			<-e.release // models a synchronous write still returning after cancellation
			close(e.finished)
		})
	}
	return tool.Response{Output: "ok"}, nil
}

type controllerFixture struct {
	server    *ToolServer
	http      *httptest.Server
	authority *testControllerAuthority
	executor  *controllerTestExecutor
	identity  ControllerIdentity
	ctx       context.Context
}

func newControllerFixture(t *testing.T) *controllerFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	a := &testControllerAuthority{grants: make(map[string]ControllerGrant), identities: make(map[string]ControllerIdentity), denied: make(map[string]bool), calls: make(map[string]int)}
	e := &controllerTestExecutor{entered: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	s := NewControlledToolServer(e, "resource", "incarnation", a)
	a.identity = s.ControllerIdentity()
	mux := http.NewServeMux()
	mux.Handle("/ws", s.Handler())
	mux.Handle("/status", s.ControllerStatusHandler())
	h := httptest.NewServer(mux)
	f := &controllerFixture{s, h, a, e, a.identity, ctx}
	t.Cleanup(func() {
		select {
		case <-e.release:
		default:
			close(e.release)
		}
		drain, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := s.ShutdownControllers(drain); err != nil {
			t.Errorf("cleanup controller drain: %v", err)
		}
		h.Close()
		cancel()
	})
	return f
}

func (f *controllerFixture) issue(credential string, epoch uint64) ControllerGrant {
	return f.authority.issue(credential, epoch, time.Now().Add(time.Minute))
}

func (f *controllerFixture) connect(credential string, grant ControllerGrant) (*WSTransport, error) {
	return NewControlledWSTransport(f.ctx, "ws"+strings.TrimPrefix(f.http.URL, "http")+"/ws", credential, f.identity, grant)
}

func controllerAwait(t *testing.T, ctx context.Context, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatal("controller test did not settle:", ctx.Err())
	}
}

func (f *controllerFixture) waitEpoch(t *testing.T, epoch uint64) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		f.server.controller.mu.Lock()
		high := f.server.controller.high.Epoch
		f.server.controller.mu.Unlock()
		if high == epoch {
			return
		}
		select {
		case <-ticker.C:
		case <-f.ctx.Done():
			t.Fatal("controller epoch not reserved:", epoch)
		}
	}
}

type controllerConnectResult struct {
	transport *WSTransport
	err       error
}

func (f *controllerFixture) asyncConnect(credential string, grant ControllerGrant) <-chan controllerConnectResult {
	result := make(chan controllerConnectResult, 1)
	go func() {
		tr, err := f.connect(credential, grant)
		result <- controllerConnectResult{tr, err}
	}()
	return result
}

func (f *controllerFixture) receive(t *testing.T, result <-chan controllerConnectResult, success bool) *WSTransport {
	t.Helper()
	select {
	case r := <-result:
		if r.transport != nil {
			t.Cleanup(func() { _ = r.transport.Close() })
		}
		if (r.err == nil) != success {
			t.Fatalf("connection success=%t; error=%v", success, r.err)
		}
		return r.transport
	case <-f.ctx.Done():
		t.Fatal("connection did not complete:", f.ctx.Err())
		return nil
	}
}

func (f *controllerFixture) startHold(t *testing.T, a *WSTransport) <-chan error {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		_, err := a.Send(f.ctx, tool.Request{ToolName: "hold"})
		result <- err
	}()
	controllerAwait(t, f.ctx, f.executor.entered)
	return result
}

func TestControllerTakeoverJoinsForegroundAndFencesOldSocket(t *testing.T) {
	f := newControllerFixture(t)
	a := f.receive(t, f.asyncConnect("a", f.issue("a", 1)), true)
	hold := f.startHold(t, a)
	// Queue both executable work and shared connection-control mutation behind
	// the blocked callback. Neither may dispatch after replacement.
	if err := a.writeMsg(f.ctx, message{Type: "request", ID: "queued", Request: &tool.Request{ToolName: "mark"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.writeMsg(f.ctx, message{Type: "set_active_tools", ID: "queued-control", ActiveTools: []string{"hold"}}); err != nil {
		t.Fatal(err)
	}
	bResult := f.asyncConnect("b", f.issue("b", 2))
	controllerAwait(t, f.ctx, f.executor.cancelled)
	select {
	case r := <-bResult:
		if r.transport != nil {
			_ = r.transport.Close()
		}
		t.Fatal("successor finished admission before old callback returned:", r.err)
	default:
	}
	if _, err := a.Send(f.ctx, tool.Request{ToolName: "mark"}); !IsTransportError(err) {
		t.Fatal("old controller remained usable:", err)
	}
	close(f.executor.release)
	b := f.receive(t, bResult, true)
	if _, err := b.Send(f.ctx, tool.Request{ToolName: "mark"}); err != nil {
		t.Fatal(err)
	}
	if count := f.executor.calls.Load(); count != 2 {
		t.Fatal("stale queued callback dispatched; calls:", count)
	}
	select {
	case err := <-hold:
		if !IsTransportError(err) {
			t.Fatal("interrupted work must report unknown transport outcome:", err)
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
}

func TestControllerConcurrentHigherTakeoverSupersedesDrainingAdmission(t *testing.T) {
	f := newControllerFixture(t)
	a := f.receive(t, f.asyncConnect("a", f.issue("a", 1)), true)
	f.startHold(t, a)
	bResult := f.asyncConnect("b", f.issue("b", 2))
	controllerAwait(t, f.ctx, f.executor.cancelled)
	cResult := f.asyncConnect("c", f.issue("c", 3))
	f.waitEpoch(t, 3)
	close(f.executor.release)
	f.receive(t, bResult, false)
	c := f.receive(t, cResult, true)
	if _, err := c.Send(f.ctx, tool.Request{ToolName: "mark"}); err != nil {
		t.Fatal(err)
	}
	// This fake deliberately keeps old grants valid to exercise the resource's
	// local high-water rejection independently of host lease validation.
	f.receive(t, f.asyncConnect("a", f.authority.grants["a"]), false)
	f.receive(t, f.asyncConnect("b", f.authority.grants["b"]), false)
}

func TestControllerAdmissionRequiresTrustedCurrentAuthority(t *testing.T) {
	for _, test := range []struct {
		name       string
		credential string
		setup      func(*controllerFixture)
	}{
		{name: "missing"},
		{name: "forged high epoch", credential: `{"epoch":999999,"admission_id":"client-uuid"}`},
		{name: "expired", credential: "expired", setup: func(f *controllerFixture) { f.authority.issue("expired", 99, time.Now().Add(-time.Second)) }},
		{name: "lease revoked", credential: "revoked", setup: func(f *controllerFixture) {
			f.issue("revoked", 99)
			f.authority.denied["revoked"] = true
		}},
		{name: "authority unavailable", credential: "outage", setup: func(f *controllerFixture) {
			f.issue("outage", 99)
			f.authority.validate = func(credential string, _ int) error {
				if credential == "outage" {
					return errors.New("authority unavailable")
				}
				return nil
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newControllerFixture(t)
			if test.setup != nil {
				test.setup(f)
			}
			a := f.receive(t, f.asyncConnect("a", f.issue("a", 1)), true)
			headers := make(http.Header)
			headers.Set(ControllerCredentialHeader, test.credential)
			conn, _, err := websocket.Dial(f.ctx, "ws"+strings.TrimPrefix(f.http.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: headers})
			if conn != nil {
				_ = conn.CloseNow()
			}
			if err == nil {
				t.Fatal("unauthorized controller accepted")
			}
			if _, err := a.Send(f.ctx, tool.Request{ToolName: "mark"}); err != nil {
				t.Fatal("rejected admission disturbed current controller:", err)
			}
		})
	}
}

func TestControllerRevalidatesAfterDrain(t *testing.T) {
	f := newControllerFixture(t)
	a := f.receive(t, f.asyncConnect("a", f.issue("a", 1)), true)
	f.startHold(t, a)
	bResult := f.asyncConnect("b", f.issue("b", 2))
	controllerAwait(t, f.ctx, f.executor.cancelled)
	f.authority.mu.Lock()
	f.authority.denied["b"] = true
	f.authority.mu.Unlock()
	close(f.executor.release)
	f.receive(t, bResult, false)
	if count := f.executor.calls.Load(); count != 1 {
		t.Fatal("revoked successor dispatched:", count)
	}
	// Failure is closed, but a new authorized epoch can recover after drain.
	c := f.receive(t, f.asyncConnect("c", f.issue("c", 3)), true)
	if _, err := c.Send(f.ctx, tool.Request{ToolName: "mark"}); err != nil {
		t.Fatal(err)
	}
}

func TestControllerDispatchUsesLocalAdmissionNotUndeliveredIssuerState(t *testing.T) {
	f := newControllerFixture(t)
	a := f.receive(t, f.asyncConnect("a", f.issue("a", 1)), true)
	f.authority.mu.Lock()
	f.authority.denied["a"] = true
	f.authority.mu.Unlock()
	// A host-authorized message delayed after its live check may execute until
	// replacement is delivered. The executor does not claim online revocation.
	if _, err := a.Send(f.ctx, tool.Request{ToolName: "mark"}); err != nil {
		t.Fatal("undelivered issuer change affected local admission:", err)
	}
	if count := f.executor.calls.Load(); count != 1 {
		t.Fatal("authorized in-flight message did not execute:", count)
	}
}

func TestControllerLostReadyAndEqualEpochNeverReattach(t *testing.T) {
	f := newControllerFixture(t)
	grant := f.issue("a", 1)
	headers := make(http.Header)
	headers.Set(ControllerCredentialHeader, "a")
	conn, _, err := websocket.Dial(f.ctx, "ws"+strings.TrimPrefix(f.http.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	// Lose READY without reading it. The server's admission remains consumed.
	_ = conn.CloseNow()
	f.receive(t, f.asyncConnect("a", grant), false)
	req, err := http.NewRequestWithContext(f.ctx, http.MethodGet, f.http.URL+"/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(ControllerCredentialHeader, "a")
	resp, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var status ControllerStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil || resp.StatusCode != http.StatusOK || !sameAdmission(status.Grant, grant) {
		t.Fatalf("status: %+v code=%d error=%v", status, resp.StatusCode, err)
	}
	if status.Phase != "active" && status.Phase != "consumed" {
		t.Fatal("unexpected lost READY status:", status.Phase)
	}
	b := f.receive(t, f.asyncConnect("b", f.issue("b", 2)), true)
	if _, err := b.Send(f.ctx, tool.Request{ToolName: "mark"}); err != nil {
		t.Fatal(err)
	}
	if count := f.executor.calls.Load(); count != 1 {
		t.Fatal("lost handshake replayed a command:", count)
	}
}

func TestControllerRestartRequiresRegisteredBoot(t *testing.T) {
	f := newControllerFixture(t)
	grant := f.issue("a", 1)
	s := NewControlledToolServer(f.executor, f.identity.ResourceID, f.identity.Incarnation, f.authority)
	if s.ControllerIdentity().BootID == f.identity.BootID {
		t.Fatal("restart reused boot identity")
	}
	h := httptest.NewServer(s.Handler())
	defer h.Close()
	tr, err := NewControlledWSTransport(f.ctx, "ws"+strings.TrimPrefix(h.URL, "http"), "a", s.ControllerIdentity(), grant)
	if tr != nil {
		_ = tr.Close()
	}
	if err == nil {
		t.Fatal("unregistered process accepted old admission")
	}
	// Trusted reinitialization may register the new process, but that cannot
	// retarget previously issued credentials to its new boot identity.
	f.authority.mu.Lock()
	f.authority.identity = s.ControllerIdentity()
	f.authority.mu.Unlock()
	tr, err = NewControlledWSTransport(f.ctx, "ws"+strings.TrimPrefix(h.URL, "http"), "a", s.ControllerIdentity(), grant)
	if tr != nil {
		_ = tr.Close()
	}
	if err == nil {
		t.Fatal("old credential retargeted onto registered replacement process")
	}
	grant = f.issue("new-boot", 2)
	tr, err = NewControlledWSTransport(f.ctx, "ws"+strings.TrimPrefix(h.URL, "http"), "new-boot", s.ControllerIdentity(), grant)
	if err != nil {
		t.Fatal("new host-authorized boot admission failed:", err)
	}
	defer tr.Close()
	defer func() {
		if err := s.ShutdownControllers(f.ctx); err != nil {
			t.Error(err)
		}
	}()
}

func TestControllerGrantDeadlineCancelsAndJoins(t *testing.T) {
	f := newControllerFixture(t)
	grant := f.authority.issue("a", 1, time.Now().Add(500*time.Millisecond))
	a := f.receive(t, f.asyncConnect("a", grant), true)
	f.startHold(t, a)
	controllerAwait(t, f.ctx, f.executor.cancelled)
	bResult := f.asyncConnect("b", f.issue("b", 2))
	f.waitEpoch(t, 2)
	select {
	case r := <-bResult:
		if r.transport != nil {
			_ = r.transport.Close()
		}
		t.Fatal("expiry bypassed callback drain:", r.err)
	default:
	}
	close(f.executor.release)
	f.receive(t, bResult, true)
}

func TestControllerShutdownDeadlineDoesNotClaimDrain(t *testing.T) {
	f := newControllerFixture(t)
	a := f.receive(t, f.asyncConnect("a", f.issue("a", 1)), true)
	f.startHold(t, a)
	ctx, cancel := context.WithTimeout(f.ctx, 30*time.Millisecond)
	defer cancel()
	if err := f.server.ShutdownControllers(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("shutdown claimed unjoined callback settlement:", err)
	}
	controllerAwait(t, f.ctx, f.executor.cancelled)
	f.receive(t, f.asyncConnect("b", f.issue("b", 2)), false)
	close(f.executor.release)
	if err := f.server.ShutdownControllers(f.ctx); err != nil {
		t.Fatal(err)
	}
}

func TestControllerConcurrentEqualEpochHasOneSocket(t *testing.T) {
	f := newControllerFixture(t)
	grant := f.issue("same", 1)
	results := []<-chan controllerConnectResult{f.asyncConnect("same", grant), f.asyncConnect("same", grant)}
	var winner *WSTransport
	winners := 0
	for _, result := range results {
		select {
		case r := <-result:
			if r.transport != nil {
				winner = r.transport
				winners++
				t.Cleanup(func() { _ = r.transport.Close() })
			} else if r.err == nil {
				t.Fatal("rejected admission did not return an error")
			}
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		}
	}
	if winners != 1 {
		t.Fatal("equal epoch admitted socket count:", winners)
	}
	if _, err := winner.Send(f.ctx, tool.Request{ToolName: "mark"}); err != nil {
		t.Fatal("equal-epoch retry evicted established controller:", err)
	}
}

func TestControllerTakeoverResetsConnectionState(t *testing.T) {
	f := newControllerFixture(t)
	a := f.receive(t, f.asyncConnect("a", f.issue("a", 1)), true)
	if err := a.SetRules(f.ctx, []bus.PermissionRule{{Permission: "*", Pattern: "*", Action: "allow"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.SetActiveTools(f.ctx, []string{"hold"}); err != nil {
		t.Fatal(err)
	}
	if err := a.PushAnswers(f.ctx, [][]string{{"old answer"}}); err != nil {
		t.Fatal(err)
	}
	b := f.receive(t, f.asyncConnect("b", f.issue("b", 2)), true)
	available, err := b.FetchTools(f.ctx)
	if err != nil || len(available) != 2 {
		t.Fatalf("old controller tool filter leaked: %+v %v", available, err)
	}
	err = f.server.PermissionManager().Ask(f.ctx, bus.PermissionRequest{Permission: "write", Patterns: []string{"anything"}})
	var needed *bus.ErrPermissionNeeded
	if !errors.As(err, &needed) {
		t.Fatal("old permission rules leaked:", err)
	}
	_, err = f.server.QuestionManager().Ask(f.ctx, bus.AskInput{})
	var question *bus.ErrQuestionNeeded
	if !errors.As(err, &question) {
		t.Fatal("old queued answers leaked:", err)
	}
}

func TestControllerTransportLossDoesNotReplayOrBypassDrain(t *testing.T) {
	f := newControllerFixture(t)
	a := f.receive(t, f.asyncConnect("a", f.issue("a", 1)), true)
	result := f.startHold(t, a)
	_ = a.conn.CloseNow()
	controllerAwait(t, f.ctx, f.executor.cancelled)
	select {
	case err := <-result:
		if !IsTransportError(err) {
			t.Fatal("transport loss did not report unknown outcome:", err)
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	bResult := f.asyncConnect("b", f.issue("b", 2))
	f.waitEpoch(t, 2)
	select {
	case r := <-bResult:
		if r.transport != nil {
			_ = r.transport.Close()
		}
		t.Fatal("transport loss was treated as callback settlement:", r.err)
	default:
	}
	close(f.executor.release)
	f.receive(t, bResult, true)
	if count := f.executor.calls.Load(); count != 1 {
		t.Fatal("interrupted work was replayed:", count)
	}
}

func TestControllerTakeoverJoinsActualBashForeground(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("direct child settlement assertion uses Linux procfs")
	}
	f := newControllerFixture(t)
	dir := t.TempDir()
	f.server.executor = tool.NewLocalExecutor(tool.Set{"bash": tools.Bash(dir)}, nil)
	a := f.receive(t, f.asyncConnect("a", f.issue("a", 1)), true)
	if err := a.SetRules(f.ctx, []bus.PermissionRule{{Permission: "*", Pattern: "*", Action: "allow"}}); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(dir, "foreground.pid")
	input, err := json.Marshal(tools.BashInput{Command: "echo $$ > " + pidPath + "; sleep 10", Description: "bounded foreground cancellation fixture", Timeout: 12000})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := a.Send(f.ctx, tool.Request{ToolName: "bash", Input: input})
		result <- err
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	var pid string
	for pid == "" {
		if data, err := os.ReadFile(pidPath); err == nil {
			pid = strings.TrimSpace(string(data))
		}
		select {
		case <-ticker.C:
		case <-f.ctx.Done():
			t.Fatal("foreground shell did not start:", f.ctx.Err())
		}
	}
	if _, err := strconv.Atoi(pid); err != nil {
		t.Fatal("invalid child PID:", pid)
	}
	b := f.receive(t, f.asyncConnect("b", f.issue("b", 2)), true)
	if _, err := os.Stat(filepath.Join("/proc", pid)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("READY preceded direct foreground shell wait completion:", pid, err)
	}
	if _, err := b.FetchTools(f.ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !IsTransportError(err) {
			t.Fatal("interrupted command response must remain unknown:", err)
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
}

func TestControllerInterruptedHandshakeKeepsDrainAndConsumesAdmission(t *testing.T) {
	f := newControllerFixture(t)
	a := f.receive(t, f.asyncConnect("a", f.issue("a", 1)), true)
	f.startHold(t, a)
	grant := f.issue("b", 2)
	ctx, cancel := context.WithCancel(f.ctx)
	result := make(chan error, 1)
	go func() {
		tr, err := NewControlledWSTransport(ctx, "ws"+strings.TrimPrefix(f.http.URL, "http")+"/ws", "b", f.identity, grant)
		if tr != nil {
			_ = tr.Close()
		}
		result <- err
	}()
	controllerAwait(t, f.ctx, f.executor.cancelled)
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled admission succeeded before drain")
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	f.receive(t, f.asyncConnect("b", grant), false)
	cResult := f.asyncConnect("c", f.issue("c", 3))
	f.waitEpoch(t, 3)
	select {
	case r := <-cResult:
		if r.transport != nil {
			_ = r.transport.Close()
		}
		t.Fatal("aborted handshake lost original drain barrier:", r.err)
	default:
	}
	close(f.executor.release)
	f.receive(t, cResult, true)
}

func TestControllerTakeoverKeepsDeliberateBackgroundProcess(t *testing.T) {
	f := newControllerFixture(t)
	dir := t.TempDir()
	f.server.executor = tool.NewLocalExecutor(tool.Set{"bash": tools.Bash(dir)}, nil)
	a := f.receive(t, f.asyncConnect("a", f.issue("a", 1)), true)
	allow := []bus.PermissionRule{{Permission: "*", Pattern: "*", Action: "allow"}}
	if err := a.SetRules(f.ctx, allow); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "background-finished")
	input, err := json.Marshal(tools.BashInput{Command: fmt.Sprintf("setsid bash -c 'sleep 0.5; echo kept > %s' >/dev/null 2>&1 &", marker), Description: "bounded deliberate background process", Timeout: 3000})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := a.Send(f.ctx, tool.Request{ToolName: "bash", Input: input})
	if err != nil || resp.IsError || resp.Metadata["exitCode"] != float64(0) {
		t.Fatalf("background launch: %+v %v", resp, err)
	}
	b := f.receive(t, f.asyncConnect("b", f.issue("b", 2)), true)
	// Admission clears old controller permissions; the host configures the new
	// controller explicitly. It does not kill a successful detached launch.
	if err := b.SetRules(f.ctx, allow); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if contents, err := os.ReadFile(marker); err == nil && string(contents) == "kept\n" {
			return
		}
		select {
		case <-ticker.C:
		case <-f.ctx.Done():
			t.Fatal("deliberate background process did not retain resource lifetime")
		}
	}
}

// Resource lifecycle belongs in the generic executor; host authorization and
// routing implementations belong in their embedding product, not this module.
func TestControllerExecutorImportBoundary(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Clean(name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range f.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			for _, product := range []string{"github.com/airlockrun/airlock", "github.com/airlockrun/agentsdk"} {
				if path == product || strings.HasPrefix(path, product+"/") {
					t.Errorf("%s: executor lifecycle must remain host-agnostic; move %s authorization/routing to the embedding host's ControllerAuthority", name, path)
				}
			}
		}
	}
}
