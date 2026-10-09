package codex

import (
	"context"
	"errors"
	"time"

	"github.com/airlockrun/sol/localconfig"
)

var ErrNotLoggedIn = errors.New("codex: not logged in; run sol auth login codex")

// Credential is an OAuth credential with optional account-routing metadata.
type Credential = localconfig.OAuthCredential

// Store serializes changes to the Codex record. Returning nil deletes only that
// record; callbacks must not reenter the store.
type Store interface {
	Load(context.Context) (Credential, error)
	Update(context.Context, func(*Credential) (*Credential, error)) (*Credential, error)
}

// RefreshStore commits an accepted token exchange independently of caller
// cancellation, while keeping lock acquisition and the exchange cancellable.
type RefreshStore interface {
	Store
	UpdateRefresh(context.Context, func(*Credential) (*Credential, error)) (*Credential, error)
}

const refreshCommitTimeout = 5 * time.Second

// CredentialStore adapts the openai/codex record of a shared local store.
// All file persistence and locking belong to localconfig.
type CredentialStore struct{ store localconfig.Store }

func NewStore(store localconfig.Store) (*CredentialStore, error) {
	if store == nil {
		return nil, errors.New("codex: local configuration store is required")
	}
	return &CredentialStore{store: store}, nil
}

func (s *CredentialStore) Load(ctx context.Context) (Credential, error) {
	config, err := s.store.Load(ctx)
	if err != nil {
		return Credential{}, err
	}
	c, ok := config.Providers["openai"].OAuth["codex"]
	if !ok {
		return Credential{}, ErrNotLoggedIn
	}
	return c, nil
}

func (s *CredentialStore) Update(ctx context.Context, change func(*Credential) (*Credential, error)) (*Credential, error) {
	if change == nil {
		return nil, errors.New("codex: update callback is required")
	}
	return s.update(ctx, change, s.store.Update)
}

// UpdateRefresh detaches only the commit of a successfully changed credential.
func (s *CredentialStore) UpdateRefresh(ctx context.Context, change func(*Credential) (*Credential, error)) (*Credential, error) {
	if change == nil {
		return nil, errors.New("codex: refresh callback is required")
	}
	durable, ok := s.store.(localconfig.CommitStore)
	if !ok {
		return nil, errors.New("codex: refresh requires a durable commit store")
	}
	var cancel context.CancelFunc
	defer func() {
		if cancel != nil {
			cancel()
		}
	}()
	return s.update(ctx, change, func(ctx context.Context, apply func(*localconfig.Config) error) (localconfig.Config, error) {
		return durable.UpdateWithCommit(ctx, func(config *localconfig.Config) (context.Context, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			before, exists := config.Providers["openai"].OAuth["codex"]
			if err := apply(config); err != nil {
				return nil, err
			}
			after, remains := config.Providers["openai"].OAuth["codex"]
			if remains && (!exists || before != after) {
				commit, cleanup := context.WithTimeout(context.WithoutCancel(ctx), refreshCommitTimeout)
				cancel = cleanup
				return commit, nil
			}
			// A cached credential has no irreversible effect to protect.
			commit, cleanup := context.WithTimeout(ctx, refreshCommitTimeout)
			cancel = cleanup
			return commit, nil
		})
	})
}

func (s *CredentialStore) update(ctx context.Context, change func(*Credential) (*Credential, error), update func(context.Context, func(*localconfig.Config) error) (localconfig.Config, error)) (*Credential, error) {
	var result *Credential
	_, err := update(ctx, func(config *localconfig.Config) error {
		provider := config.Providers["openai"]
		var current *Credential
		if credential, ok := provider.OAuth["codex"]; ok {
			current = &credential
		}
		next, err := change(current)
		if err != nil {
			return err
		}
		if next == nil {
			delete(provider.OAuth, "codex")
		} else {
			if err := validateCredential(*next); err != nil {
				return err
			}
			if provider.OAuth == nil {
				provider.OAuth = make(map[string]localconfig.OAuthCredential)
			}
			provider.OAuth["codex"] = *next
			copy := *next
			result = &copy
		}
		if provider.APIKey == nil && len(provider.OAuth) == 0 {
			delete(config.Providers, "openai")
		} else {
			config.Providers["openai"] = provider
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func validateCredential(c Credential) error {
	if c.AccessToken == "" || c.RefreshToken == "" || c.ExpiresAt.IsZero() {
		return errors.New("codex: incomplete credential")
	}
	return nil
}
