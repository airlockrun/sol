package provider

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/airlockrun/goai/provider/anthropic"
	"github.com/airlockrun/goai/provider/baseten"
	"github.com/airlockrun/goai/provider/cerebras"
	"github.com/airlockrun/goai/provider/cohere"
	"github.com/airlockrun/goai/provider/deepinfra"
	"github.com/airlockrun/goai/provider/deepseek"
	"github.com/airlockrun/goai/provider/fireworks"
	"github.com/airlockrun/goai/provider/google"
	"github.com/airlockrun/goai/provider/groq"
	"github.com/airlockrun/goai/provider/huggingface"
	"github.com/airlockrun/goai/provider/mistral"
	"github.com/airlockrun/goai/provider/openai"
	"github.com/airlockrun/goai/provider/openaicompat"
	"github.com/airlockrun/goai/provider/perplexity"
	"github.com/airlockrun/goai/provider/togetherai"
	"github.com/airlockrun/goai/provider/xai"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/sol/auth/codex"
	"github.com/airlockrun/sol/localconfig"
	"github.com/airlockrun/sol/session"
)

const CodexMode = "codex"

// LocalModelOptions selects a concrete live model, never an application model slot
// or the saved default. ResolveLocalModel is the explicit credential-discovery
// boundary. Supplying Store or ConfigPath avoids OS path discovery.
type LocalModelOptions struct {
	Model string
	Auth  string
	// HTTPClient is copied with redirects disabled for credentialed requests.
	HTTPClient *http.Client
	UserAgent  string
	SessionID  string
	Store      localconfig.Store
	ConfigPath string
	// LookupEnv defaults to os.LookupEnv only during explicit resolution.
	LookupEnv func(string) (string, bool)
	// BaseURL overrides API-key transport. Codex uses CodexURL instead.
	BaseURL string
	// CodexURL and Issuer default to the production protocol endpoints.
	CodexURL string
	Issuer   string
	// Limits overrides catalog capability maxima. Unknown models require it.
	Limits *session.ModelLimits
}

// ResolveLocalModel creates a configured live model and its compaction limits.
// It never selects a stored default, proxies through a host, or initiates login.
// Resolution reads credentials but performs no LLM request or OAuth refresh;
// Codex refresh runs at the request boundary with durable store serialization.
// Mock models are deliberately outside this API.
func ResolveLocalModel(ctx context.Context, opts LocalModelOptions) (stream.Model, session.ModelLimits, error) {
	if err := ctx.Err(); err != nil {
		return nil, session.ModelLimits{}, err
	}
	selection := localconfig.ModelSelection{Model: opts.Model, Auth: opts.Auth}
	if err := selection.Validate(); err != nil {
		return nil, session.ModelLimits{}, err
	}
	id, modelID := ParseModel(opts.Model)
	if id == "mock" {
		return nil, session.ModelLimits{}, errors.New("local model resolution requires an explicit real provider; inject mock models separately")
	}
	if opts.Auth != localconfig.APIKeyMode && opts.Auth != CodexMode {
		return nil, session.ModelLimits{}, errors.New("local model auth must be api-key or codex")
	}
	if opts.HTTPClient == nil || opts.UserAgent == "" || opts.SessionID == "" {
		return nil, session.ModelLimits{}, errors.New("local model requires HTTPClient, UserAgent and SessionID")
	}
	if opts.Store != nil && opts.ConfigPath != "" {
		return nil, session.ModelLimits{}, errors.New("local model accepts Store or ConfigPath, not both")
	}
	if opts.Auth == CodexMode && (id != "openai" || opts.BaseURL != "") {
		return nil, session.ModelLimits{}, errors.New("Codex requires openai and its dedicated Responses endpoint")
	}
	if opts.Auth == localconfig.APIKeyMode && (opts.CodexURL != "" || opts.Issuer != "") {
		return nil, session.ModelLimits{}, errors.New("Codex endpoints require codex authentication")
	}
	if opts.BaseURL != "" {
		if err := validateLocalURL(opts.BaseURL); err != nil {
			return nil, session.ModelLimits{}, err
		}
	}
	if opts.CodexURL != "" {
		if err := validateLocalURL(opts.CodexURL); err != nil {
			return nil, session.ModelLimits{}, err
		}
	}
	if opts.Issuer != "" {
		if err := validateLocalURL(opts.Issuer); err != nil {
			return nil, session.ModelLimits{}, err
		}
	}
	if info, ok := GetProviderInfo(id); ok && info.ID != id {
		return nil, session.ModelLimits{}, fmt.Errorf("local model requires canonical provider ID %s", info.ID)
	}
	if opts.Auth == localconfig.APIKeyMode && nativeLocalProvider(id) {
		if _, ok := localProviderFactories[id]; !ok {
			return nil, session.ModelLimits{}, fmt.Errorf("provider %s has no local language-model transport with explicit HTTP client support", id)
		}
		if id == "baseten" && opts.BaseURL != "" {
			return nil, session.ModelLimits{}, errors.New("local Baseten language models use deployment URLs and do not support BaseURL")
		}
	}
	limits, err := localLimits(id, modelID, opts.Limits)
	if err != nil {
		return nil, session.ModelLimits{}, err
	}
	store := opts.Store
	if store == nil {
		path := opts.ConfigPath
		if path == "" {
			path, err = localconfig.DefaultPath()
			if err != nil {
				return nil, session.ModelLimits{}, err
			}
		}
		store, err = localconfig.NewFileStore(path)
		if err != nil {
			return nil, session.ModelLimits{}, err
		}
	}
	config, err := store.Load(ctx)
	if err != nil {
		return nil, session.ModelLimits{}, err
	}
	if err := config.Validate(); err != nil {
		return nil, session.ModelLimits{}, err
	}
	if opts.Auth == CodexMode {
		credentials, err := codex.NewStore(store)
		if err != nil {
			return nil, session.ModelLimits{}, err
		}
		if _, err := credentials.Load(ctx); err != nil {
			return nil, session.ModelLimits{}, err
		}
		issuer := opts.Issuer
		if issuer == "" {
			issuer = codex.Issuer
		}
		client, err := codex.NewClient(codex.ClientOptions{Store: credentials, HTTPClient: opts.HTTPClient, Issuer: issuer, ClientID: codex.ClientID, UserAgent: opts.UserAgent})
		if err != nil {
			return nil, session.ModelLimits{}, err
		}
		endpoint := opts.CodexURL
		if endpoint == "" {
			endpoint = CodexURL
		}
		model, err := NewCodexModel(modelID, CodexOptions{Credentials: client, HTTPClient: opts.HTTPClient, URL: endpoint, UserAgent: opts.UserAgent, SessionID: opts.SessionID})
		return model, limits, err
	}
	lookup := opts.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	key, err := ResolveLocalAPIKey(config, id, lookup)
	if err != nil {
		return nil, session.ModelLimits{}, err
	}
	model, err := localAPIModel(id, modelID, key, opts)
	if err != nil {
		return nil, session.ModelLimits{}, err
	}
	return model, limits, nil
}

// ResolveLocalAPIKey shares the CLI's environment-over-store policy without any
// file discovery. An explicitly present empty environment value disables a key.
func ResolveLocalAPIKey(config localconfig.Config, id string, lookup func(string) (string, bool)) (string, error) {
	if !localconfig.ValidID(id) || lookup == nil {
		return "", errors.New("local API key requires a provider ID and environment lookup")
	}
	env := GetEnvVarName(id)
	if key, present := lookup(env); present {
		if strings.TrimSpace(key) == "" {
			return "", fmt.Errorf("%s is explicitly empty", env)
		}
		if strings.ContainsAny(key, "\r\n") {
			return "", fmt.Errorf("%s must contain a single API key", env)
		}
		return key, nil
	}
	if credential := config.Providers[id].APIKey; credential != nil {
		if strings.TrimSpace(credential.Key) == "" || strings.ContainsAny(credential.Key, "\r\n") {
			return "", errors.New("invalid stored API key")
		}
		return credential.Key, nil
	}
	return "", fmt.Errorf("configure an API key with sol auth set-key %s or set %s", id, env)
}

func localLimits(id, modelID string, override *session.ModelLimits) (session.ModelLimits, error) {
	info, ok := GetModelInfo(id, modelID)
	if ok && info.Kind != "" && info.Kind != KindLanguage {
		return session.ModelLimits{}, errors.New("local model must be a language model")
	}
	if override != nil {
		if err := override.Validate(true); err != nil {
			return session.ModelLimits{}, err
		}
		return *override, nil
	}
	if !ok || info.Limit == nil {
		return session.ModelLimits{}, fmt.Errorf("model %s/%s has no catalog limits; supply Limits explicitly", id, modelID)
	}
	limits := session.ModelLimits{Context: info.Limit.Context, Input: info.Limit.Input, Output: info.Limit.Output}
	if err := limits.Validate(true); err != nil {
		return session.ModelLimits{}, fmt.Errorf("invalid catalog model limits: %w", err)
	}
	return limits, nil
}

func nativeLocalProvider(id string) bool {
	if _, ok := localProviderFactories[id]; ok {
		return true
	}
	_, dedicated := providerFactories[id]
	return dedicated && id != "openai" && id != "openrouter" && id != "openai-compatible"
}

// Local factories preserve provider-specific protocols and accept the resolver's
// authoritative client. A dedicated provider without a factory fails before
// credential discovery instead of falling back to a global HTTP client.
var localProviderFactories = map[string]func(modelID, key, base string, client *http.Client) stream.Model{
	"anthropic": func(modelID, key, base string, client *http.Client) stream.Model {
		return anthropic.New(anthropic.Options{APIKey: key, BaseURL: base, HTTPClient: client}).Model(modelID)
	},
	"google": func(modelID, key, base string, client *http.Client) stream.Model {
		return google.New(google.Options{APIKey: key, BaseURL: base, HTTPClient: client}).Model(modelID)
	},
	"cohere": func(modelID, key, base string, client *http.Client) stream.Model {
		return cohere.New(cohere.Options{APIKey: key, BaseURL: base, HTTPClient: client}).Model(modelID)
	},
	"mistral": func(modelID, key, base string, client *http.Client) stream.Model {
		return mistral.New(mistral.Options{APIKey: key, BaseURL: base, HTTPClient: client}).Model(modelID)
	},
	"deepseek": func(modelID, key, base string, client *http.Client) stream.Model {
		return deepseek.New(deepseek.Options{APIKey: key, BaseURL: base, HTTPClient: client}).Model(modelID)
	},
	"groq": func(modelID, key, base string, client *http.Client) stream.Model {
		return groq.New(groq.Options{APIKey: key, BaseURL: base, HTTPClient: client}).Model(modelID)
	},
	"fireworks-ai": func(modelID, key, base string, client *http.Client) stream.Model {
		return fireworks.New(fireworks.Options{APIKey: key, BaseURL: base, HTTPClient: client}).Model(modelID)
	},
	"cerebras": func(modelID, key, base string, client *http.Client) stream.Model {
		return cerebras.New(cerebras.Options{APIKey: key, BaseURL: base, HTTPClient: client}).Model(modelID)
	},
	"perplexity": func(modelID, key, base string, client *http.Client) stream.Model {
		return perplexity.New(perplexity.Options{APIKey: key, BaseURL: base, HTTPClient: client}).Model(modelID)
	},
	"togetherai": func(modelID, key, base string, client *http.Client) stream.Model {
		return togetherai.New(togetherai.Options{APIKey: key, BaseURL: base, HTTPClient: client}).Model(modelID)
	},
	"deepinfra": func(modelID, key, base string, client *http.Client) stream.Model {
		return deepinfra.New(deepinfra.Options{APIKey: key, BaseURL: base, HTTPClient: client}).Model(modelID)
	},
	"baseten": func(modelID, key, base string, client *http.Client) stream.Model {
		return baseten.New(baseten.Options{APIKey: key, HTTPClient: client}).Model(modelID)
	},
	"xai": func(modelID, key, base string, client *http.Client) stream.Model {
		return xai.New(xai.Options{APIKey: key, BaseURL: base, HTTPClient: client}).Model(modelID)
	},
	"huggingface": func(modelID, key, base string, client *http.Client) stream.Model {
		return huggingface.New(huggingface.Options{APIKey: key, BaseURL: base, HTTPClient: client}).Model(modelID)
	},
}

func localAPIModel(id, modelID, key string, opts LocalModelOptions) (stream.Model, error) {
	client := *opts.HTTPClient
	// Refuse redirects even when the caller supplies a permissive policy: custom
	// auth headers and 307/308 request bodies must stay at the selected endpoint.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	base := strings.TrimRight(opts.BaseURL, "/")
	if id == "openai" {
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		model := openai.NewResponsesModel(modelID, openai.ResponsesConfig{Provider: "openai.responses", URL: base + "/responses", HTTPClient: &client,
			ConfigureRequest: func(req *http.Request) error {
				req.Header.Set("Authorization", "Bearer "+key)
				req.Header.Set("User-Agent", opts.UserAgent)
				req.Header.Set("session-id", opts.SessionID)
				return nil
			},
		})
		return model, nil
	}
	if nativeLocalProvider(id) {
		factory, ok := localProviderFactories[id]
		if !ok {
			return nil, errors.New("provider has no local language-model transport with explicit HTTP client support")
		}
		model := factory(modelID, key, base, &client)
		if model == nil {
			return nil, errors.New("provider does not support a language model")
		}
		return &localHeaderModel{model: model, userAgent: opts.UserAgent, sessionID: opts.SessionID}, nil
	}
	if base == "" {
		if id == "openrouter" {
			base = OpenRouterBaseURL
		} else if info, ok := GetProviderInfo(id); ok {
			base = strings.TrimRight(info.API, "/")
		}
	}
	if base == "" {
		return nil, errors.New("local compatible provider requires an explicit BaseURL or catalog API endpoint")
	}
	if err := validateLocalURL(base); err != nil {
		return nil, err
	}
	model := openaicompat.New(openaicompat.Options{ProviderID: id, APIKey: key, BaseURL: base, HTTPClient: &client, SupportsStructuredOutputs: id != "openai-compatible"}).Model(modelID)
	return &localHeaderModel{model: model, userAgent: opts.UserAgent, sessionID: opts.SessionID}, nil
}

type localHeaderModel struct {
	model                stream.Model
	userAgent, sessionID string
}

func (m *localHeaderModel) ID() string       { return m.model.ID() }
func (m *localHeaderModel) Provider() string { return m.model.Provider() }
func (m *localHeaderModel) Stream(ctx context.Context, opts *stream.CallOptions) (<-chan stream.Event, error) {
	if opts == nil {
		return nil, errors.New("local model call options are required")
	}
	copy := *opts
	copy.Headers = maps.Clone(opts.Headers)
	if copy.Headers == nil {
		copy.Headers = make(map[string]string)
	}
	for key := range copy.Headers {
		if strings.EqualFold(key, "User-Agent") || strings.EqualFold(key, "session-id") {
			delete(copy.Headers, key)
		}
	}
	copy.Headers["User-Agent"] = m.userAgent
	copy.Headers["session-id"] = m.sessionID
	return m.model.Stream(ctx, &copy)
}

func validateLocalURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("local model endpoint must be an HTTP(S) URL without credentials, query or fragment")
	}
	return nil
}
