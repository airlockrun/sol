package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/provider"
)

func TestWebSearchDirectProviderOptions(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/v1/tools/search" || r.Header.Get("Authorization") != "Bearer test" {
			t.Errorf("unexpected request %s %v", r.URL, r.Header)
		}
		json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"title": "result", "url": "https://example.com", "summary": "detail"}}})
	}))
	defer srv.Close()
	search, ok := WebSearch("deepseek", provider.Options{APIKey: "test", BaseURL: srv.URL + "/v1", HTTPClient: srv.Client()})
	if !ok {
		t.Fatal("missing deepseek search capability")
	}
	result, err := search.Execute(t.Context(), json.RawMessage(`{"query":"q"}`), tool.CallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !called || result.Output == "" {
		t.Fatal("search did not use configured endpoint")
	}
}
