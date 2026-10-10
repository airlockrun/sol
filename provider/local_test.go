package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/airlockrun/goai"
	goaierrors "github.com/airlockrun/goai/errors"
	"github.com/airlockrun/goai/message"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/sol/auth/codex"
	"github.com/airlockrun/sol/localconfig"
	"github.com/airlockrun/sol/provider"
	"github.com/airlockrun/sol/session"
)

func localTestStore(t *testing.T) *localconfig.FileStore {
	t.Helper()
	store, err := localconfig.NewFileStore(filepath.Join(t.TempDir(), "sol", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func localTestOptions(store localconfig.Store) provider.LocalModelOptions {
	return provider.LocalModelOptions{Model: "personal/openai/gpt-4o-mini", Store: store, HTTPClient: http.DefaultClient, UserAgent: "resolver/test", SessionID: "explicit-session", LookupEnv: func(string) (string, bool) { return "", false }}
}

type localCountingTransport struct {
	calls *atomic.Int32
	base  http.RoundTripper
}

func (t localCountingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return t.base.RoundTrip(req)
}

func localStreamText(t *testing.T, model stream.Model) string {
	t.Helper()
	result, err := goai.StreamText(t.Context(), stream.Input{Model: model, Instructions: "test instructions", Messages: []goai.Message{goai.NewUserMessage("hello")}, MaxRetries: 0, MaxRetriesSet: true})
	if err != nil {
		t.Fatal(err)
	}
	for range result.FullStream {
	}
	text, err := result.Text()
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func TestResolveLocalModelAPIKey(t *testing.T) {
	for _, tc := range []struct {
		name, env, want  string
		present, success bool
	}{
		{"stored", "", "stored-test-key", false, true}, {"environment", "env-test-key", "env-test-key", true, true}, {"explicit empty", "", "", true, false}, {"missing explicit environment", "", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := localTestStore(t)
			_, err := store.Update(t.Context(), func(config *localconfig.Config) error {
				p := localconfig.ProviderConfig{Auth: "api-key", Key: "stored-test-key"}
				if tc.name != "stored" {
					p.Key = ""
					p.KeyEnv = "EXPLICIT_OPENAI_KEY"
				}
				config.Providers["personal/openai"] = p
				config.DefaultModel = "personal/openai/gpt-5.4"
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer "+tc.want || r.Header.Get("User-Agent") != "resolver/test" || r.Header.Get("session-id") != "explicit-session" {
					t.Error("wrong configured transport or credentials")
				}
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if body["model"] != "gpt-4o-mini" {
					t.Error("saved default substituted for explicit model")
				}
				writeCodexText(w, "resolved")
			}))
			defer server.Close()
			opts := localTestOptions(store)
			var transported atomic.Int32
			client := *server.Client()
			transport := client.Transport
			if transport == nil {
				transport = http.DefaultTransport
			}
			client.Transport = localCountingTransport{calls: &transported, base: transport}
			opts.HTTPClient = &client
			opts.BaseURL = server.URL
			opts.LookupEnv = func(name string) (string, bool) {
				if name != "EXPLICIT_OPENAI_KEY" {
					t.Error("wrong env variable")
				}
				return tc.env, tc.present
			}
			model, limits, err := provider.ResolveLocalModel(t.Context(), opts)
			if (err == nil) != tc.success {
				t.Fatal(err)
			}
			if !tc.success {
				if model != nil || strings.Contains(err.Error(), "test-key") {
					t.Fatal("failed resolution leaked credentials")
				}
				return
			}
			if err := limits.Validate(true); err != nil {
				t.Fatal(err)
			}
			info, ok := provider.GetModelInfo("openai", "gpt-4o-mini")
			if !ok || limits.Context != info.Limit.Context || limits.Output != info.Limit.Output {
				t.Fatal("catalog limits not returned")
			}
			if calls.Load() != 0 {
				t.Fatal("resolution sent a model request")
			}
			if localStreamText(t, model) != "resolved" || calls.Load() != 1 || transported.Load() != 1 {
				t.Fatal("model did not use configured transport")
			}
		})
	}
}

func TestResolveLocalModelConfigPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sol", "config.json")
	store, err := localconfig.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(t.Context(), func(config *localconfig.Config) error {
		config.Providers["personal/openai"] = localconfig.ProviderConfig{Auth: "api-key", Key: "path-key"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer path-key" {
			t.Error("wrong config file used")
		}
		writeCodexText(w, "path")
	}))
	defer server.Close()
	opts := localTestOptions(nil)
	opts.ConfigPath = path
	opts.BaseURL = server.URL
	opts.HTTPClient = server.Client()
	model, _, err := provider.ResolveLocalModel(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if localStreamText(t, model) != "path" {
		t.Fatal("path config not used")
	}
}

func TestResolveLocalModelExplicitOSDiscovery(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	t.Setenv("OPENAI_API_KEY", "explicit-env-test-key")
	path, err := localconfig.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	opts := localTestOptions(nil)
	opts.LookupEnv = nil
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("options construction accessed configuration")
	}
	store, _ := localconfig.NewFileStore(path)
	if _, err := store.Update(t.Context(), func(config *localconfig.Config) error {
		config.Providers["personal/openai"] = localconfig.ProviderConfig{Auth: "api-key", KeyEnv: "OPENAI_API_KEY"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	model, limits, err := provider.ResolveLocalModel(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if model.ID() != "gpt-4o-mini" || limits.Context == 0 {
		t.Fatal("explicit OS resolution failed")
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatal("explicit resolve did not load user store")
	}
}

type unreadStore struct{ calls atomic.Int32 }

func (s *unreadStore) Load(context.Context) (localconfig.Config, error) {
	s.calls.Add(1)
	return localconfig.Config{}, errors.New("unexpected credential discovery")
}
func (s *unreadStore) Update(context.Context, func(*localconfig.Config) error) (localconfig.Config, error) {
	s.calls.Add(1)
	return localconfig.Config{}, errors.New("unexpected credential update")
}

func TestResolveLocalModelRejectsBeforeDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*provider.LocalModelOptions)
	}{
		{"empty model", func(o *provider.LocalModelOptions) { o.Model = "" }},
		{"slot name", func(o *provider.LocalModelOptions) { o.Model = "reasoner" }},
		{"mock", func(o *provider.LocalModelOptions) { o.Model = "mock/scripted" }},
		{"missing client", func(o *provider.LocalModelOptions) { o.HTTPClient = nil }},
		{"missing user agent", func(o *provider.LocalModelOptions) { o.UserAgent = "" }},
		{"missing session", func(o *provider.LocalModelOptions) { o.SessionID = "" }},
		{"ambiguous store", func(o *provider.LocalModelOptions) { o.ConfigPath = "/explicit/config.json" }},
		{"invalid URL", func(o *provider.LocalModelOptions) { o.BaseURL = "https://user:secret@example.invalid" }},
		{"unknown limits", func(o *provider.LocalModelOptions) { o.Model = "personal/openai/local-unknown-model" }},
		{"invalid override", func(o *provider.LocalModelOptions) { o.Limits = &session.ModelLimits{Output: 100} }},
		{"provider alias", func(o *provider.LocalModelOptions) {
			o.Model = "personal/fireworks/deployment"
			o.Limits = &session.ModelLimits{Input: 1000}
		}},
		{"unsupported Baseten endpoint override", func(o *provider.LocalModelOptions) {
			o.Model = "personal/baseten/deployment"
			o.BaseURL = "http://localhost:1234"
			o.Limits = &session.ModelLimits{Input: 1000}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &unreadStore{}
			opts := localTestOptions(store)
			tc.change(&opts)
			if _, _, err := provider.ResolveLocalModel(t.Context(), opts); err == nil {
				t.Fatal("invalid explicit selection accepted")
			}
			if store.calls.Load() != 0 {
				t.Fatal("invalid selection accessed credentials")
			}
		})
	}
	store := &unreadStore{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := provider.ResolveLocalModel(ctx, localTestOptions(store)); !errors.Is(err, context.Canceled) || store.calls.Load() != 0 {
		t.Fatal("canceled resolve accessed credentials", err)
	}
}

func TestResolveLocalModelCompatibleOverride(t *testing.T) {
	store := localTestStore(t)
	_, err := store.Update(t.Context(), func(config *localconfig.Config) error {
		config.Providers["personal/openai-compatible"] = localconfig.ProviderConfig{Auth: "api-key", Key: "compat-key"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer compat-key" || r.Header.Get("User-Agent") != "resolver/test" {
			t.Error("incorrect compatible transport")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"compatible\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	limits := session.ModelLimits{Input: 16000, Output: 2000}
	opts := localTestOptions(store)
	opts.Model = "personal/openai-compatible/deployment"
	opts.BaseURL = server.URL
	opts.HTTPClient = server.Client()
	opts.Limits = &limits
	model, got, err := provider.ResolveLocalModel(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got != limits || localStreamText(t, model) != "compatible" {
		t.Fatal("override or compatible transport lost")
	}
}

func TestResolveLocalModelNativeAnthropic(t *testing.T) {
	store := localTestStore(t)
	_, err := store.Update(t.Context(), func(config *localconfig.Config) error {
		config.Providers["personal/anthropic"] = localconfig.ProviderConfig{Auth: "api-key", Key: "anthropic-key"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" || r.Header.Get("x-api-key") != "anthropic-key" || r.Header.Get("User-Agent") != "resolver/test" {
			t.Error("native provider transport lost")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range []string{`{"type":"message_start","message":{"id":"msg","usage":{"input_tokens":1}}}`, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"native"}}`, `{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`, `{"type":"message_stop"}`} {
			fmt.Fprintf(w, "data: %s\n\n", frame)
		}
	}))
	defer server.Close()
	opts := localTestOptions(store)
	opts.Model = "personal/anthropic/claude-test"
	opts.BaseURL = server.URL
	var transported atomic.Int32
	opts.HTTPClient = &http.Client{Transport: localCountingTransport{calls: &transported, base: http.DefaultTransport}}
	opts.Limits = &session.ModelLimits{Input: 8000}
	model, _, err := provider.ResolveLocalModel(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if localStreamText(t, model) != "native" || transported.Load() != 1 {
		t.Fatal("native model did not use its explicit HTTP client")
	}
}

type localRoundTripFunc func(*http.Request) (*http.Response, error)

func (f localRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestResolveLocalModelRejectsAPIKeyRedirects(t *testing.T) {
	// Include every dedicated language provider, plus Responses and compatible
	// routes. Custom clients must remain authoritative through nested constructors.
	for _, id := range []string{
		"anthropic", "google", "cohere", "mistral", "deepseek", "groq",
		"fireworks-ai", "cerebras", "perplexity", "togetherai", "deepinfra",
		"baseten", "xai", "huggingface", "openai", "openrouter",
		"openai-compatible",
	} {
		t.Run(id, func(t *testing.T) {
			for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
				t.Run(fmt.Sprint(status), func(t *testing.T) {
					var targetCalls, sourceCalls, transported, redirectPolicyCalls atomic.Int32
					target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						targetCalls.Add(1)
						_, _ = io.Copy(io.Discard, r.Body)
						w.WriteHeader(http.StatusNoContent)
					}))
					defer target.Close()
					// Different hostnames exercise cross-host redirect handling, not
					// merely a port change on the same host.
					targetURL := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)
					const key = "redirect-test-key"
					source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						sourceCalls.Add(1)
						body, err := io.ReadAll(r.Body)
						if err != nil || !strings.Contains(string(body), "private-test-prompt") || r.Method != http.MethodPost {
							t.Error("initial request lost its method or body")
						}
						switch id {
						case "anthropic":
							if r.Header.Get("x-api-key") != key {
								t.Error("initial request lost x-api-key")
							}
						case "google":
							if r.URL.Query().Get("key") != key {
								t.Error("initial request lost its API key")
							}
						case "baseten":
							if r.Header.Get("Authorization") != "Api-Key "+key {
								t.Error("initial request lost Baseten auth")
							}
						default:
							if r.Header.Get("Authorization") != "Bearer "+key {
								t.Error("initial request lost bearer auth")
							}
						}
						http.Redirect(w, r, targetURL+"/capture", status)
					}))
					defer source.Close()
					sourceURL, err := url.Parse(source.URL)
					if err != nil {
						t.Fatal(err)
					}
					transport := http.DefaultTransport.(*http.Transport).Clone()
					transport.Proxy = nil
					defer transport.CloseIdleConnections()
					client := &http.Client{Timeout: 5 * time.Second,
						CheckRedirect: func(*http.Request, []*http.Request) error {
							redirectPolicyCalls.Add(1)
							return nil
						},
						Transport: localRoundTripFunc(func(req *http.Request) (*http.Response, error) {
							transported.Add(1)
							if id == "baseten" && req.URL.Host == "model-deployment.api.baseten.co" {
								// Baseten encodes its deployment URL in the model ID.
								// Route that request to loopback without DNS or TLS I/O.
								req = req.Clone(req.Context())
								req.URL.Scheme, req.URL.Host = sourceURL.Scheme, sourceURL.Host
							}
							if req.URL.Host != sourceURL.Host && req.URL.Host != strings.TrimPrefix(targetURL, "http://") {
								return nil, errors.New("test transport refuses non-loopback endpoint")
							}
							return transport.RoundTrip(req)
						}),
					}
					store := localTestStore(t)
					_, err = store.Update(t.Context(), func(config *localconfig.Config) error {
						config.Providers["personal/"+id] = localconfig.ProviderConfig{Auth: "api-key", Key: key}
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
					opts := localTestOptions(store)
					opts.Model, opts.BaseURL, opts.HTTPClient = "personal/"+id+"/deployment", source.URL, client
					if id == "baseten" {
						opts.BaseURL = ""
					}
					opts.Limits = &session.ModelLimits{Input: 8000}
					model, _, err := provider.ResolveLocalModel(t.Context(), opts)
					if err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					ch, failure := model.Stream(ctx, &stream.CallOptions{Messages: []message.Message{
						message.NewSystemMessage("test instructions"), message.NewUserMessage("private-test-prompt"),
					}})
					if failure == nil {
						for event := range ch {
							if e, ok := event.Data.(stream.ErrorEvent); ok {
								failure = e.Error
							}
						}
					}
					var apiError *goaierrors.APICallError
					if !errors.As(failure, &apiError) || apiError.StatusCode != status {
						t.Fatalf("redirect failure = %v, want HTTP %d", failure, status)
					}
					if sourceCalls.Load() != 1 || transported.Load() != 1 || targetCalls.Load() != 0 || redirectPolicyCalls.Load() != 0 {
						t.Fatalf("source=%d transport=%d target=%d caller policy=%d", sourceCalls.Load(), transported.Load(), targetCalls.Load(), redirectPolicyCalls.Load())
					}
					if client.Timeout != 5*time.Second || client.CheckRedirect(nil, nil) != nil || redirectPolicyCalls.Load() != 1 {
						t.Fatal("resolver mutated the supplied HTTP client")
					}
				})
			}
		})
	}
}

func TestResolveLocalModelRejectsNonLanguageFactoriesBeforeDiscovery(t *testing.T) {
	for _, id := range []string{"elevenlabs", "deepgram", "assemblyai", "revai", "lmnt", "hume", "fal", "luma", "replicate"} {
		t.Run(id, func(t *testing.T) {
			store := &unreadStore{}
			opts := localTestOptions(store)
			opts.Model = "personal/" + id + "/deployment"
			opts.Limits = &session.ModelLimits{Input: 8000}
			if _, _, err := provider.ResolveLocalModel(t.Context(), opts); err == nil || !strings.Contains(err.Error(), "explicit HTTP client support") {
				t.Fatalf("unsupported factory error = %v", err)
			}
			if store.calls.Load() != 0 {
				t.Fatal("unsupported factory accessed credentials")
			}
		})
	}
}

func TestResolveLocalModelCodexRefresh(t *testing.T) {
	store := localTestStore(t)
	_, err := store.Update(t.Context(), func(config *localconfig.Config) error {
		config.Providers["work/openai"] = localconfig.ProviderConfig{Auth: "codex", Credentials: &localconfig.OAuthCredential{AccessToken: "expired", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Hour), AccountID: "account"}}
		config.Providers["personal/openai"] = localconfig.ProviderConfig{Auth: "api-key", Key: "must-not-use"}
		config.DefaultModel = "personal/openai/gpt-4o"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var refreshes, requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			refreshes.Add(1)
			r.ParseForm()
			if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "old-refresh" {
				t.Error("unexpected OAuth flow")
			}
			fmt.Fprint(w, `{"access_token":"new-access","refresh_token":"rotated","expires_in":3600}`)
		case "/codex/responses":
			requests.Add(1)
			if r.Header.Get("Authorization") != "Bearer new-access" || r.Header.Get("ChatGPT-Account-Id") != "account" {
				t.Error("wrong Codex auth")
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["instructions"] != "test instructions" || body["store"] != false {
				t.Error("Codex adapter policy lost")
			}
			writeCodexText(w, "codex-resolved")
		default:
			t.Error("resolution initiated login or unexpected request")
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	opts := localTestOptions(store)
	opts.Model = "work/openai/gpt-5.4"
	opts.HTTPClient = server.Client()
	opts.CodexURL = server.URL + "/codex/responses"
	opts.Issuer = server.URL
	opts.LookupEnv = func(string) (string, bool) { t.Error("Codex consulted API-key environment"); return "", false }
	model, limits, err := provider.ResolveLocalModel(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if limits.Context == 0 || refreshes.Load() != 0 || requests.Load() != 0 {
		t.Fatal("resolve performed OAuth or model request")
	}
	if localStreamText(t, model) != "codex-resolved" || refreshes.Load() != 1 || requests.Load() != 1 {
		t.Fatal("configured Codex request failed")
	}
	config, err := store.Load(t.Context())
	if err != nil || config.Providers["work/openai"].Credentials.RefreshToken != "rotated" || config.Providers["personal/openai"].Key != "must-not-use" || config.DefaultModel != "personal/openai/gpt-4o" {
		t.Fatal("refresh lost shared configuration", err)
	}
}

func TestResolveLocalModelLoggedOut(t *testing.T) {
	store := localTestStore(t)
	if _, err := store.Update(t.Context(), func(c *localconfig.Config) error {
		c.Providers["personal/openai"] = localconfig.ProviderConfig{Auth: "codex"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	opts := localTestOptions(store)
	if _, _, err := provider.ResolveLocalModel(t.Context(), opts); !errors.Is(err, codex.ErrNotLoggedIn) {
		t.Fatal("missing device auth did not fail explicitly", err)
	}
}

func TestNamedAPIKeyAccounts(t *testing.T) {
	store := localTestStore(t)
	if _, err := store.Update(t.Context(), func(c *localconfig.Config) error {
		c.Providers["work/openai"] = localconfig.ProviderConfig{Auth: "api-key", Key: "work-key"}
		c.Providers["personal/openai"] = localconfig.ProviderConfig{Auth: "api-key", Key: "personal-key"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []string{"work/openai", "personal/openai"} {
		t.Run(entry, func(t *testing.T) {
			want := "work-key"
			if entry == "personal/openai" {
				want = "personal-key"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+want {
					t.Error("account credentials crossed")
				}
				writeCodexText(w, "account")
			}))
			defer server.Close()
			opts := localTestOptions(store)
			opts.Model, opts.BaseURL = entry+"/gpt-4o-mini", server.URL
			opts.LookupEnv = func(string) (string, bool) { t.Error("inline key consulted environment"); return "global-key", true }
			model, _, err := provider.ResolveLocalModel(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if localStreamText(t, model) != "account" {
				t.Fatal("wrong model")
			}
		})
	}
	opts := localTestOptions(store)
	opts.Model = "missing/openai/gpt-4o-mini"
	opts.LookupEnv = func(string) (string, bool) { t.Error("unknown entry consulted environment"); return "global-key", true }
	if _, _, err := provider.ResolveLocalModel(t.Context(), opts); err == nil {
		t.Fatal("unknown entry silently created")
	}
	for _, entry := range []string{"work/fireworks", "work/unknown-provider", "work/OpenAI"} {
		if provider.ValidateLocalEntry(entry) == nil {
			t.Fatal("accepted noncanonical provider")
		}
	}
}
