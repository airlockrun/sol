package codex

import (
	"context"
	"errors"
	"time"

	"github.com/airlockrun/sol/localconfig"
)

var ErrNotLoggedIn = errors.New("codex: account disconnected; run sol auth login ENTRY --method codex")

type Credential = localconfig.OAuthCredential
type Store interface {
	Load(context.Context) (Credential, error)
	Update(context.Context, func(*Credential) (*Credential, error)) (*Credential, error)
}
type RefreshStore interface {
	Store
	UpdateRefresh(context.Context, func(*Credential) (*Credential, error)) (*Credential, error)
}

const refreshCommitTimeout = 5 * time.Second

// CredentialStore scopes all credential operations to one named OpenAI account.
type CredentialStore struct {
	store  localconfig.Store
	locker localconfig.EntryLocker
	entry  string
}

func NewStore(store localconfig.Store, entry string) (*CredentialStore, error) {
	_, provider, err := localconfig.ParseEntry(entry)
	if err != nil {
		return nil, err
	}
	if provider != "openai" {
		return nil, errors.New("codex: requires an openai entry")
	}
	locker, ok := store.(localconfig.EntryLocker)
	if !ok || store == nil {
		return nil, errors.New("codex: account-locking local configuration store is required")
	}
	return &CredentialStore{store: store, locker: locker, entry: entry}, nil
}

func (s *CredentialStore) Load(ctx context.Context) (Credential, error) {
	config, err := s.store.Load(ctx)
	if err != nil {
		return Credential{}, err
	}
	p := config.Providers[s.entry]
	if p.Auth != "codex" || p.Credentials == nil {
		return Credential{}, ErrNotLoggedIn
	}
	return *p.Credentials, nil
}
func (s *CredentialStore) Update(ctx context.Context, change func(*Credential) (*Credential, error)) (*Credential, error) {
	return s.update(ctx, change, false)
}
func (s *CredentialStore) UpdateRefresh(ctx context.Context, change func(*Credential) (*Credential, error)) (*Credential, error) {
	return s.update(ctx, change, true)
}
func (s *CredentialStore) update(ctx context.Context, change func(*Credential) (*Credential, error), refresh bool) (*Credential, error) {
	if change == nil {
		return nil, errors.New("codex: update callback is required")
	}
	if refresh {
		if _, ok := s.store.(localconfig.CommitStore); !ok {
			return nil, errors.New("codex: refresh requires durable commit store")
		}
	}
	unlock, err := s.locker.LockEntry(ctx, s.entry)
	if err != nil {
		return nil, err
	}
	defer unlock()
	config, err := s.store.Load(ctx)
	if err != nil {
		return nil, err
	}
	before, exists := config.Providers[s.entry]
	if exists && before.Auth != "codex" && refresh {
		return nil, ErrNotLoggedIn
	}
	var current *Credential
	if before.Auth == "codex" && before.Credentials != nil {
		copy := *before.Credentials
		current = &copy
	}
	// Token exchanges hold only the account lock; other accounts can refresh concurrently.
	next, err := change(current)
	if err != nil {
		return nil, err
	}
	if next != nil {
		if err := validateCredential(*next); err != nil {
			return nil, err
		}
	}
	commit := ctx
	var cancel context.CancelFunc
	if refresh && next != nil && (current == nil || before.Credentials == nil || *next != *before.Credentials) {
		commit, cancel = context.WithTimeout(context.WithoutCancel(ctx), refreshCommitTimeout)
		defer cancel()
	}
	apply := func(config *localconfig.Config) error {
		actual, present := config.Providers[s.entry]
		if present != exists || !sameBinding(actual, before) {
			return errors.New("codex: account changed during credential operation")
		}
		config.Providers[s.entry] = localconfig.ProviderConfig{Auth: "codex", Credentials: next}
		return nil
	}
	if refresh && cancel != nil {
		durable, ok := s.store.(localconfig.CommitStore)
		if !ok {
			return nil, errors.New("codex: refresh requires durable commit store")
		}
		_, err = durable.UpdateWithCommit(commit, func(config *localconfig.Config) (context.Context, error) { return commit, apply(config) })
	} else {
		_, err = s.store.Update(commit, apply)
	}
	if err != nil {
		return nil, err
	}
	return next, nil
}
func sameBinding(a, b localconfig.ProviderConfig) bool {
	if a.Auth != b.Auth || a.Key != b.Key || a.KeyEnv != b.KeyEnv || a.BaseURL != b.BaseURL {
		return false
	}
	if a.Credentials == nil || b.Credentials == nil {
		return a.Credentials == b.Credentials
	}
	return *a.Credentials == *b.Credentials
}
func validateCredential(c Credential) error {
	if c.AccessToken == "" || c.RefreshToken == "" || c.ExpiresAt.IsZero() {
		return errors.New("codex: incomplete credential")
	}
	return nil
}
