package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol"
	"github.com/airlockrun/sol/auth/codex"
	"github.com/airlockrun/sol/localconfig"
	"github.com/airlockrun/sol/provider"
	"github.com/airlockrun/sol/tools"
)

func localCodex(store localconfig.Store, entry string) (*codex.Client, error) {
	credentials, err := codex.NewStore(store, entry)
	if err != nil {
		return nil, err
	}
	return codex.NewClient(codex.ClientOptions{Store: credentials, HTTPClient: &http.Client{Timeout: 30 * time.Second}, Issuer: codex.Issuer, ClientID: codex.ClientID, UserAgent: "sol/" + sol.Version})
}

type deviceClient interface {
	StartDeviceAuth(context.Context) (codex.DeviceAuthorization, error)
	CompleteDeviceAuth(context.Context, codex.DeviceAuthorization) (codex.Credential, error)
}

func runAuth(ctx context.Context, args []string, output io.Writer, client deviceClient, store localconfig.Store, secret func(context.Context, bool) (string, error)) error {
	const usage = "usage: sol auth login ENTRY [--method codex]; sol auth set-key ENTRY [--stdin]; sol auth status [ENTRY]; sol auth logout|remove-key ENTRY"
	if len(args) == 0 {
		return errors.New(usage)
	}
	if args[0] == "status" && len(args) <= 2 {
		config, err := store.Load(ctx)
		if err != nil {
			return err
		}
		var entries []string
		if len(args) == 2 {
			if err := provider.ValidateLocalEntry(args[1]); err != nil {
				return err
			}
			if _, ok := config.Providers[args[1]]; !ok {
				return errors.New("unknown provider entry")
			}
			entries = []string{args[1]}
		} else {
			for entry := range config.Providers {
				entries = append(entries, entry)
			}
			sort.Strings(entries)
		}
		for _, entry := range entries {
			p := config.Providers[entry]
			connected := p.Key != "" || p.KeyEnv != "" || p.Credentials != nil
			if _, err := fmt.Fprintf(output, "%s: auth=%s configured=%t", entry, p.Auth, connected); err != nil {
				return err
			}
			if p.Credentials != nil {
				if _, err := fmt.Fprintf(output, " expires=%s", p.Credentials.ExpiresAt.Format(time.RFC3339)); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintln(output); err != nil {
				return err
			}
		}
		return nil
	}
	if len(args) < 2 {
		return errors.New(usage)
	}
	entry := args[1]
	if err := provider.ValidateLocalEntry(entry); err != nil {
		return err
	}
	switch args[0] {
	case "login":
		method := ""
		if len(args) == 4 && args[2] == "--method" && args[3] == "codex" {
			method = "codex"
		} else if len(args) != 2 {
			return errors.New(usage)
		}
		config, err := store.Load(ctx)
		if err != nil {
			return err
		}
		if method == "" {
			method = config.Providers[entry].Auth
		}
		_, id, _ := localconfig.ParseEntry(entry)
		if method != "codex" || id != "openai" {
			return errors.New("login requires an openai entry with --method codex or configured codex auth")
		}
		if client == nil {
			client, err = localCodex(store, entry)
			if err != nil {
				return err
			}
		}
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
		credentials, err := codex.NewStore(store, entry)
		if err != nil {
			return err
		}
		if _, err := credentials.Update(ctx, func(*codex.Credential) (*codex.Credential, error) { return &credential, nil }); err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "Connected %s using codex. Use %s/MODEL.\n", entry, entry)
		return err
	case "set-key", "remove-key", "logout":
		stdin := len(args) == 3 && args[2] == "--stdin" && args[0] == "set-key"
		if len(args) != 2 && !stdin {
			return errors.New(usage)
		}
		var key string
		if args[0] == "set-key" {
			if secret == nil {
				return errors.New("API key input is required")
			}
			var err error
			key, err = secret(ctx, stdin)
			if err != nil {
				return err
			}
			if key == "" {
				return errors.New("API key must not be empty")
			}
		}
		locker, ok := store.(localconfig.EntryLocker)
		if !ok {
			return errors.New("account-locking store is required")
		}
		unlock, err := locker.LockEntry(ctx, entry)
		if err != nil {
			return err
		}
		defer unlock()
		_, err = store.Update(ctx, func(config *localconfig.Config) error {
			if args[0] == "set-key" {
				config.Providers[entry] = localconfig.ProviderConfig{Auth: localconfig.APIKeyMode, Key: key, BaseURL: config.Providers[entry].BaseURL}
				return nil
			}
			p, exists := config.Providers[entry]
			if !exists {
				return errors.New("unknown provider entry")
			}
			if args[0] == "remove-key" && p.Auth != localconfig.APIKeyMode {
				return errors.New("remove-key requires an api-key entry")
			}
			p.Key, p.KeyEnv, p.Credentials = "", "", nil
			config.Providers[entry] = p
			return nil
		})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "Local credentials updated for %s.\n", entry)
		return err
	default:
		return errors.New(usage)
	}
}
func resolveSearch(auth, providerID, apiKey string) (tool.Tool, bool) {
	if auth != "codex" && apiKey != "" && provider.SearchBackend(providerID) != "" {
		return tools.WebSearch(providerID, apiKey)
	}
	return tool.Tool{}, false
}
