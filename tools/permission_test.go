package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/airlockrun/sol/bus"
)

func TestPermissionManager_RuleAllow(t *testing.T) {
	b := bus.New()
	pm := bus.NewPermissionManager(b)

	pm.SetRules([]bus.PermissionRule{
		{Permission: "read", Pattern: "*", Action: "allow"},
		{Permission: "edit", Pattern: "*.txt", Action: "allow"},
	})

	ctx := context.Background()

	// read should be allowed
	err := pm.Ask(ctx, bus.PermissionRequest{
		SessionID:  "test",
		Permission: "read",
		Patterns:   []string{"anything.go"},
	})
	if err != nil {
		t.Errorf("expected allow for read, got %v", err)
	}

	// edit *.txt should be allowed
	err = pm.Ask(ctx, bus.PermissionRequest{
		SessionID:  "test",
		Permission: "edit",
		Patterns:   []string{"test.txt"},
	})
	if err != nil {
		t.Errorf("expected allow for edit *.txt, got %v", err)
	}
}

func TestPermissionManager_RuleDeny(t *testing.T) {
	b := bus.New()
	pm := bus.NewPermissionManager(b)

	pm.SetRules([]bus.PermissionRule{
		{Permission: "bash", Pattern: "*", Action: "deny"},
	})

	ctx := context.Background()
	err := pm.Ask(ctx, bus.PermissionRequest{
		SessionID:  "test",
		Permission: "bash",
		Patterns:   []string{"rm -rf /"},
	})

	if err == nil {
		t.Fatal("expected deny error")
	}
	var denied *bus.PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Errorf("expected PermissionDeniedError, got %T: %v", err, err)
	}
}

func TestPermissionManager_NoRuleReturnsErrPermissionNeeded(t *testing.T) {
	b := bus.New()
	pm := bus.NewPermissionManager(b)
	// No rules set — default is "ask"

	ctx := context.Background()
	err := pm.Ask(ctx, bus.PermissionRequest{
		SessionID:  "test",
		Permission: "edit",
		Patterns:   []string{"test.txt"},
		ToolCallID: "call_1",
	})

	if err == nil {
		t.Fatal("expected ErrPermissionNeeded")
	}
	var permErr *bus.ErrPermissionNeeded
	if !errors.As(err, &permErr) {
		t.Errorf("expected ErrPermissionNeeded, got %T: %v", err, err)
	}
	if permErr.Permission != "edit" {
		t.Errorf("expected permission 'edit', got %q", permErr.Permission)
	}
	if permErr.ToolCallID != "call_1" {
		t.Errorf("expected toolCallID 'call_1', got %q", permErr.ToolCallID)
	}
}

func TestPermissionManager_AlwaysPublishesEvent(t *testing.T) {
	b := bus.New()
	pm := bus.NewPermissionManager(b)
	pm.SetRules([]bus.PermissionRule{
		{Permission: "*", Pattern: "*", Action: "allow"},
	})

	var eventCount int
	b.Subscribe(bus.PermissionAsked, func(e bus.Event) {
		eventCount++
	})

	ctx := context.Background()
	pm.Ask(ctx, bus.PermissionRequest{
		SessionID:  "test",
		Permission: "read",
		Patterns:   []string{"file.txt"},
	})

	if eventCount != 1 {
		t.Errorf("expected 1 event, got %d", eventCount)
	}
}

func TestPermissionManager_AddRule(t *testing.T) {
	b := bus.New()
	pm := bus.NewPermissionManager(b)

	ctx := context.Background()

	// No rules — should return ErrPermissionNeeded
	err := pm.Ask(ctx, bus.PermissionRequest{
		SessionID:  "test",
		Permission: "edit",
		Patterns:   []string{"test.txt"},
	})
	var permErr *bus.ErrPermissionNeeded
	if !errors.As(err, &permErr) {
		t.Fatalf("expected ErrPermissionNeeded, got %v", err)
	}

	// Add allow rule
	pm.AddRule(bus.PermissionRule{Permission: "edit", Pattern: "*.txt", Action: "allow"})

	// Now should be allowed
	err = pm.Ask(ctx, bus.PermissionRequest{
		SessionID:  "test",
		Permission: "edit",
		Patterns:   []string{"test.txt"},
	})
	if err != nil {
		t.Errorf("expected allow after AddRule, got %v", err)
	}
}

func TestPermissionManager_FatalToolError(t *testing.T) {
	// ErrPermissionNeeded should implement FatalToolError
	err := &bus.ErrPermissionNeeded{Permission: "edit"}
	if !err.FatalToolError() {
		t.Error("expected FatalToolError() to return true")
	}
}

func TestPermissionManager_WildcardRules(t *testing.T) {
	b := bus.New()
	pm := bus.NewPermissionManager(b)

	pm.SetRules([]bus.PermissionRule{
		{Permission: "*", Pattern: "*", Action: "allow"},
	})

	ctx := context.Background()
	err := pm.Ask(ctx, bus.PermissionRequest{
		SessionID:  "test",
		Permission: "anything",
		Patterns:   []string{"any.pattern"},
	})
	if err != nil {
		t.Errorf("expected wildcard rule to allow, got: %v", err)
	}
}

func TestPermissionManager_ShowsDiffInEvent(t *testing.T) {
	b := bus.New()
	pm := bus.NewPermissionManager(b)
	// No rules — returns ErrPermissionNeeded, but still publishes event

	var receivedMetadata map[string]any
	b.Subscribe(bus.PermissionAsked, func(e bus.Event) {
		payload := e.Properties.(bus.PermissionAskedPayload)
		receivedMetadata = payload.Metadata
	})

	ctx := context.Background()
	pm.Ask(ctx, bus.PermissionRequest{
		SessionID:  "test",
		Permission: "edit",
		Patterns:   []string{"test.txt"},
		Metadata: map[string]any{
			"diff": "--- test.txt\n+++ test.txt\n@@ -1 +1 @@\n-old\n+new",
		},
	})

	if receivedMetadata == nil {
		t.Fatal("expected metadata in event")
	}
	diff, ok := receivedMetadata["diff"].(string)
	if !ok || !strings.Contains(diff, "-old") {
		t.Errorf("expected diff in metadata, got: %v", receivedMetadata)
	}
}
