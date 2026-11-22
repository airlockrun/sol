package websearch

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/sol/internal/generation"
)

type modelClient struct {
	backend string
	model   stream.Model
}

// IsModelBackend reports whether search uses a provider's hosted model tool.
func IsModelBackend(backend string) bool {
	switch backend {
	case "openai", "gemini", "grok", "kimi":
		return true
	}
	return false
}

// DefaultModel selects the backend's small search model from the active catalog.
// Direct search backends have no model and return an empty string.
func DefaultModel(backend string) string {
	var id, fallback string
	var tokens []string
	switch backend {
	case "openai":
		id, fallback, tokens = "openai", defaultOpenAIModel, []string{"nano", "mini"}
	case "gemini":
		id, fallback, tokens = "google", defaultGeminiModel, []string{"flash-lite", "flash"}
	case "grok":
		id, fallback, tokens = "xai", defaultGrokModel, []string{"fast", "mini", "non-reasoning"}
	case "kimi":
		id, fallback, tokens = "moonshotai", defaultKimiModel, []string{"k2"}
	default:
		return ""
	}
	if selected := pickSearchModel(id, tokens); selected != "" {
		return selected
	}
	return fallback
}

// NewModelClient uses a normally constructed model, retaining its authentication,
// endpoint and HTTP transport. Every model attempt uses the normal generation
// wall and inactivity deadlines and joins its producer before returning.
func NewModelClient(backend string, model stream.Model) Client {
	if !IsModelBackend(backend) {
		panic("websearch: unsupported model backend " + backend)
	}
	if model == nil || (reflect.ValueOf(model).Kind() == reflect.Pointer && reflect.ValueOf(model).IsNil()) {
		panic("websearch: model is required")
	}
	return &modelClient{backend: backend, model: model}
}

func (c *modelClient) Search(parent context.Context, req Request) (*Response, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, errors.New("websearch: query is required")
	}
	ctx, cancel := context.WithCancelCause(parent)
	bounded := &generation.Model{Model: c.model, Wall: generation.DefaultWall, Idle: generation.DefaultIdle, StopStep: cancel}
	defer func() { cancel(nil); bounded.Producers.Wait() }()
	var resp *Response
	var err error
	switch c.backend {
	case "openai":
		resp, err = c.searchOpenAI(ctx, bounded, req)
	case "gemini":
		resp, err = c.searchGemini(ctx, bounded, req)
	case "grok":
		resp, err = c.searchGrok(ctx, bounded, req)
	case "kimi":
		resp, err = c.searchKimi(ctx, bounded, req)
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("websearch/%s: %w", c.backend, errors.Join(err, ctx.Err(), context.Cause(ctx)))
	}
	return resp, err
}
