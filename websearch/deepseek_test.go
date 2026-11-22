package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeepSeek(t *testing.T) {
	for _, tc := range []struct {
		name, body    string
		status, count int
		want          string
		fail          bool
	}{
		{"summary", `{"results":[{"title":"DeepSeek","url":"https://example.com","summary":"long","snippet":"short"}]}`, 200, 99, "long", false},
		{"snippet", `{"results":[{"title":"DeepSeek","url":"https://example.com","summary":" ","snippet":"short"}]}`, 200, 0, "short", false},
		{"empty", `{"results":[]}`, 200, 2, "", false},
		{"malformed", `{`, 200, 2, "", true},
		{"unauthorized", `{"error":{"message":"bad key"}}`, 401, 2, "", true},
		{"rate limited", `{"error":{"message":"slow down"}}`, 429, 2, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/v1/tools/search" {
					t.Errorf("request %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer key" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("missing headers")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				count := tc.count
				if count <= 0 {
					count = 5
				}
				if count > 10 {
					count = 10
				}
				if body["query"] != "深度搜索" || body["count"] != float64(count) || len(body) != 2 {
					t.Errorf("payload: %#v", body)
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			client := NewClient(Options{Provider: "deepseek", APIKey: "key", BaseURL: srv.URL + "/v1/", HTTPClient: srv.Client()})
			resp, err := client.Search(t.Context(), Request{Query: "深度搜索", Count: tc.count})
			if tc.fail {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if resp.Provider != "deepseek" || resp.Synthesis != "" {
				t.Fatalf("response: %+v", resp)
			}
			if tc.want == "" {
				if len(resp.Results) != 0 {
					t.Fatal(resp.Results)
				}
				return
			}
			if len(resp.Results) != 1 || resp.Results[0].Snippet != tc.want {
				t.Fatal(resp.Results)
			}
		})
	}
}

func TestDeepSeekQueryValidation(t *testing.T) {
	for _, q := range []string{"", " \t\n", strings.Repeat("界", 129), string([]byte{0xff})} {
		t.Run(q, func(t *testing.T) {
			_, err := NewClient(Options{Provider: "deepseek", APIKey: "key"}).Search(context.Background(), Request{Query: q})
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
