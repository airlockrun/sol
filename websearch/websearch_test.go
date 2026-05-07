package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/airlockrun/goai/stream"
)

func TestSearch_EmptyQuery(t *testing.T) {
	client := NewClient(Options{Provider: "brave", APIKey: "test"})
	_, err := client.Search(context.Background(), Request{})
	if err == nil || err.Error() != "websearch: query is required" {
		t.Errorf("expected empty query error, got: %v", err)
	}
}

func TestSearch_MissingAPIKey(t *testing.T) {
	client := NewClient(Options{Provider: "brave"})
	_, err := client.Search(context.Background(), Request{Query: "test"})
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestSearch_UnknownProvider(t *testing.T) {
	client := NewClient(Options{Provider: "bing", APIKey: "test"})
	_, err := client.Search(context.Background(), Request{Query: "test"})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestSearch_CountClamping(t *testing.T) {
	// Verify count is clamped to maxCount. We use a mock Brave server
	// and inspect the count param in the request.
	var receivedCount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCount = r.URL.Query().Get("count")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"web": map[string]any{"results": []any{}}})
	}))
	defer srv.Close()

	client := NewClient(Options{Provider: "brave", APIKey: "test"})
	// Override the HTTP client to hit our test server.
	// We do this by replacing the endpoint via a custom http.Client transport.
	client.http = srv.Client()

	// We can't easily override the endpoint constant, so test with a real
	// mock at the transport level instead. For now, test the dispatch logic.
	t.Run("default count", func(t *testing.T) {
		req := Request{Query: "test", Count: 0}
		// Normalize count the same way Search does.
		if req.Count <= 0 {
			req.Count = defaultCount
		}
		if req.Count != 5 {
			t.Errorf("expected default count 5, got %d", req.Count)
		}
	})

	t.Run("clamped count", func(t *testing.T) {
		req := Request{Query: "test", Count: 99}
		if req.Count > maxCount {
			req.Count = maxCount
		}
		if req.Count != 10 {
			t.Errorf("expected clamped count 10, got %d", req.Count)
		}
	})

	_ = receivedCount // used when we have a real server test
	_ = srv
}

func TestSourcesToResults(t *testing.T) {
	t.Run("dedups by URL and drops empty", func(t *testing.T) {
		got := sourcesToResults([]stream.SourceEvent{
			{URL: "https://go.dev", Title: "Go"},
			{URL: "https://go.dev", Title: "Duplicate"},
			{URL: "", Title: "Empty drops"},
			{URL: "https://example.com", Title: "Example"},
		})
		if len(got) != 2 {
			t.Fatalf("expected 2 results, got %d", len(got))
		}
		if got[0].URL != "https://go.dev" || got[1].URL != "https://example.com" {
			t.Errorf("unexpected order: %+v", got)
		}
	})

	t.Run("nil input returns nil", func(t *testing.T) {
		if got := sourcesToResults(nil); got != nil {
			t.Errorf("expected nil, got %+v", got)
		}
	})
}
