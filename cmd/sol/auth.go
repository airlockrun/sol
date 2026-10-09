package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol"
	"github.com/airlockrun/sol/auth/codex"
	"github.com/airlockrun/sol/localconfig"
	"github.com/airlockrun/sol/provider"
	"github.com/airlockrun/sol/tools"
	"github.com/airlockrun/sol/websearch"
)

func localCodex(store localconfig.Store) (*codex.Client, error) {
	credentials, err := codex.NewStore(store)
	if err != nil {
		return nil, err
	}
	return codex.NewClient(codex.ClientOptions{Store: credentials, HTTPClient: &http.Client{Timeout: 30 * time.Second}, Issuer: codex.Issuer, ClientID: codex.ClientID, UserAgent: "sol/" + sol.Version})
}

type deviceClient interface {
	StartDeviceAuth(context.Context) (codex.DeviceAuthorization, error)
	CompleteDeviceAuth(context.Context, codex.DeviceAuthorization) (codex.Credential, error)
}

// runAuth receives all dependencies explicitly; tests never resolve local auth.
func runAuth(ctx context.Context, args []string, output io.Writer, client deviceClient, store localconfig.Store, secret func(context.Context, bool) (string, error)) error {
	const usage = "usage: sol auth login|status|logout codex; sol auth set-key PROVIDER [--stdin]; sol auth status|remove-key PROVIDER"
	if len(args) < 2 || !localconfig.ValidID(args[1]) {
		return errors.New(usage)
	}
	if args[0] == "set-key" {
		if args[1] == "codex" {
			return errors.New("Codex uses device login; configure OpenAI API keys under openai")
		}
		stdin := len(args) == 3 && args[2] == "--stdin"
		if len(args) != 2 && !stdin {
			return errors.New(usage)
		}
		if secret == nil {
			return errors.New("API key input is required")
		}
		key, err := secret(ctx, stdin)
		if err != nil {
			return err
		}
		_, err = store.Update(ctx, func(config *localconfig.Config) error {
			provider := config.Providers[args[1]]
			provider.APIKey = &localconfig.APIKeyCredential{Key: key}
			config.Providers[args[1]] = provider
			return nil
		})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "API key configured for %s.\n", args[1])
		return err
	}
	if len(args) != 2 {
		return errors.New(usage)
	}
	if args[0] == "remove-key" {
		if args[1] == "codex" {
			return errors.New("use sol auth logout codex for device credentials")
		}
		_, err := store.Update(ctx, func(config *localconfig.Config) error {
			provider := config.Providers[args[1]]
			provider.APIKey = nil
			if len(provider.OAuth) == 0 {
				delete(config.Providers, args[1])
			} else {
				config.Providers[args[1]] = provider
			}
			return nil
		})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "Local API key removed for %s.\n", args[1])
		return err
	}
	if args[0] == "status" && args[1] != "codex" {
		config, err := store.Load(ctx)
		if err != nil {
			return err
		}
		present := config.Providers[args[1]].APIKey != nil
		_, err = fmt.Fprintf(output, "%s: stored API key configured = %t.\n", args[1], present)
		return err
	}
	if args[1] != "codex" {
		return errors.New(usage)
	}
	credentials, err := codex.NewStore(store)
	if err != nil {
		return err
	}
	switch args[0] {
	case "login":
		ctx, cancel := context.WithTimeout(ctx, codex.LoginTimeout)
		defer cancel()
		device, err := client.StartDeviceAuth(ctx)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "Open %s\nEnter code: %s\n", device.VerificationURL, device.UserCode); err != nil {
			return err
		}
		credential, err := client.CompleteDeviceAuth(ctx, device)
		if err != nil {
			return err
		}
		if _, err := credentials.Update(ctx, func(*codex.Credential) (*codex.Credential, error) { return &credential, nil }); err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, "Logged in to Codex.")
		return err
	case "status":
		credential, err := credentials.Load(ctx)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "Codex credential present; access expires %s.\n", credential.ExpiresAt.Format(time.RFC3339))
		return err
	case "logout":
		if _, err := credentials.Update(ctx, func(*codex.Credential) (*codex.Credential, error) { return nil, nil }); err != nil {
			return err
		}
		_, err := fmt.Fprintln(output, "Local Codex credentials removed.")
		return err
	default:
		return errors.New(usage)
	}
}

func validateAuthMode(auth, providerID, modelID, proxyURL, baseURL string, selectedModel bool) error {
	switch auth {
	case "api-key":
		return nil
	case "codex":
		if providerID != "openai" || modelID == "" || !selectedModel {
			return errors.New("Codex requires an explicit or configured openai/<Codex-supported model>")
		}
		if proxyURL != "" {
			return errors.New("-auth codex requires direct local mode; unset AIRLOCK_API_URL")
		}
		if baseURL != "" {
			return errors.New("-auth codex uses its dedicated backend; unset OPENAI_BASE_URL")
		}
		return nil
	default:
		return errors.New("-auth must be api-key or codex")
	}
}

func codexModels(modelID, titleID string, explicitTitle bool, opts provider.CodexOptions) (stream.Model, stream.Model, error) {
	model, err := provider.NewCodexModel(modelID, opts)
	if err != nil {
		return nil, nil, err
	}
	if !explicitTitle {
		return model, model, nil
	}
	providerID, titleID := provider.ParseModel(titleID)
	if providerID != "openai" {
		return nil, nil, errors.New("Codex title model must use openai")
	}
	title, err := provider.NewCodexModel(titleID, opts)
	return model, title, err
}

func resolveSearch(auth, providerID, apiKey string, config localconfig.Config) (tool.Tool, bool) {
	if auth != "codex" && apiKey != "" && provider.SearchBackend(providerID) != "" {
		return tools.WebSearch(providerID, apiKey)
	}
	// Codex device tokens never reach API-key search backends. Independent search
	// providers honor the same explicit environment-over-store precedence.
	for _, id := range []string{"brave", "perplexity"} {
		key, err := provider.ResolveLocalAPIKey(config, id, os.LookupEnv)
		if err == nil {
			return websearch.NewTool(websearch.NewClient(websearch.Options{Provider: id, APIKey: key})), true
		}
	}
	return tool.Tool{}, false
}
