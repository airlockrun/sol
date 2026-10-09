package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/airlockrun/sol/localconfig"
)

func testClient(t *testing.T, server *httptest.Server, store Store) *Client {
	t.Helper()
	c, err := NewClient(ClientOptions{HTTPClient: server.Client(), Store: store, Issuer: server.URL, ClientID: "test-client", UserAgent: "sol/test"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func testStore(t *testing.T) *CredentialStore {
	t.Helper()
	shared, err := localconfig.NewFileStore(filepath.Join(t.TempDir(), "sol", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(shared)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func jwt(data string) string {
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(data)) + ".signature"
}
func save(t *testing.T, store Store, c Credential) {
	t.Helper()
	if _, err := store.Update(t.Context(), func(*Credential) (*Credential, error) { return &c, nil }); err != nil {
		t.Fatal(err)
	}
}

func TestClientDeviceFlow(t *testing.T) {
	var polls atomic.Int32
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("User-Agent") != "sol/test" {
			t.Error("unexpected method or user agent")
		}
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["client_id"] != "test-client" {
				t.Error("invalid initiation body")
			}
			w.Write([]byte(`{"device_auth_id":"device","user_code":"CODE","interval":"1"}`))
		case "/api/accounts/deviceauth/token":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["device_auth_id"] != "device" || body["user_code"] != "CODE" {
				t.Error("invalid poll body")
			}
			switch polls.Add(1) {
			case 1:
				w.WriteHeader(403)
			case 2:
				w.WriteHeader(404)
			default:
				w.Write([]byte(`{"authorization_code":"approved","code_verifier":"server-verifier"}`))
			}
		case "/oauth/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "approved" || r.Form.Get("code_verifier") != "server-verifier" || r.Form.Get("client_id") != "test-client" || r.Form.Get("redirect_uri") != issuer+"/deviceauth/callback" {
				t.Errorf("invalid exchange: %v", r.Form)
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "refresh_token": "refresh", "expires_in": 3600, "id_token": jwt(`{"https://api.openai.com/auth":{"chatgpt_account_id":"account"}}`)})
		default:
			t.Error("unexpected path")
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	issuer = server.URL
	store := testStore(t)
	c := testClient(t, server, store)
	device, err := c.StartDeviceAuth(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if device.VerificationURL != server.URL+"/codex/device" {
		t.Fatal(device)
	}
	credential, err := c.CompleteDeviceAuth(t.Context(), device)
	if err != nil {
		t.Fatal(err)
	}
	if credential.AccountID != "account" || credential.AccessToken != "access" || credential.RefreshToken != "refresh" || time.Until(credential.ExpiresAt) < 59*time.Minute {
		t.Fatal("invalid credential")
	}
	if _, err := store.Load(t.Context()); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatal("device flow must not implicitly persist", err)
	}
}

func TestClientDeviceFailures(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		status         int
	}{
		{"missing fields", `{}`, 200}, {"bad interval", `{"device_auth_id":"d","user_code":"c","interval":"garbage"}`, 200}, {"zero interval", `{"device_auth_id":"d","user_code":"c","interval":0}`, 200}, {"huge interval", `{"device_auth_id":"d","user_code":"c","interval":"100000"}`, 200}, {"bad JSON", `not-json`, 200}, {"trailing", `{} {}`, 200}, {"denied", `secret server error`, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.response)) }))
			defer server.Close()
			_, err := testClient(t, server, testStore(t)).StartDeviceAuth(t.Context())
			if err == nil {
				t.Fatal("expected failure")
			}
		})
	}
	for _, status := range []int{400, 401, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			_, err := testClient(t, server, testStore(t)).CompleteDeviceAuth(t.Context(), DeviceAuthorization{DeviceAuthID: "d", UserCode: "c", PollInterval: time.Second})
			if err == nil {
				t.Fatal("expected failure")
			}
		})
	}
}

func TestClientCancellation(t *testing.T) {
	for _, pending := range []bool{true, false} {
		t.Run(map[bool]string{true: "poll sleep", false: "request"}[pending], func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				close(started)
				if pending {
					w.WriteHeader(403)
					return
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				_, err := testClient(t, server, testStore(t)).CompleteDeviceAuth(ctx, DeviceAuthorization{DeviceAuthID: "d", UserCode: "c", PollInterval: time.Second})
				done <- err
			}()
			<-started
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation hung")
			}
		})
	}
}

func TestClientRefreshSerializesStores(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		r.ParseForm()
		if r.Form.Get("refresh_token") != "old-refresh" || r.Form.Get("grant_type") != "refresh_token" {
			t.Error("incorrect refresh")
		}
		time.Sleep(20 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]any{"access_token": jwt(`{"https://api.openai.com/auth":{"chatgpt_compute_residency":"eu"}}`), "refresh_token": "rotated", "expires_in": 3600})
	}))
	defer server.Close()
	store := testStore(t)
	save(t, store, Credential{AccessToken: "expired", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Second), AccountID: "account"})
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			independent, _ := NewStore(store.store)
			access, err := testClient(t, server, independent).Access(t.Context())
			if err != nil {
				t.Error(err)
				return
			}
			if access.AccountID != "account" || access.ComputeResidency != "eu" {
				t.Error("lost routing metadata")
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d", calls.Load())
	}
	c, err := store.Load(t.Context())
	if err != nil || c.RefreshToken != "rotated" {
		t.Fatal("rotation not persisted", err)
	}
}

func TestClientRefreshValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		success    bool
	}{
		{"omitted rotation", `{"access_token":"new"}`, true}, {"missing access", `{"refresh_token":"new"}`, false}, {"invalid expiry", `{"access_token":"new","expires_in":0}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(tc.body)) }))
			defer server.Close()
			store := testStore(t)
			save(t, store, Credential{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now(), AccountID: "account"})
			_, err := testClient(t, server, store).Access(t.Context())
			if (err == nil) != tc.success {
				t.Fatal(err)
			}
			c, err := store.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if c.RefreshToken != "refresh" || c.AccountID != "account" {
				t.Fatal("metadata lost")
			}
			if !tc.success && c.AccessToken != "old" {
				t.Fatal("failed refresh changed store")
			}
		})
	}
}

func TestClaims(t *testing.T) {
	for _, tc := range []struct{ name, data, account, region string }{
		{"root", `{"chatgpt_account_id":"root","chatgpt_compute_residency":"us"}`, "root", "us"},
		{"nested", `{"https://api.openai.com/auth":{"chatgpt_account_id":"nested","chatgpt_compute_residency":"future-region"}}`, "nested", "future-region"},
		{"unconstrained", `{"chatgpt_compute_residency":"us","https://api.openai.com/auth":{"chatgpt_compute_residency":"no_constraint"}}`, "", ""},
		{"organization", `{"organizations":[{"id":"org"}],"chatgpt_data_residency":"gb"}`, "org", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := jwt(tc.data)
			if accountID(token) != tc.account || residency(token) != tc.region {
				t.Fatal("unexpected claims")
			}
		})
	}
	if accountID("broken") != "" || residency("broken") != "" {
		t.Fatal("malformed JWT accepted")
	}
	if accountID(jwt(`{"chatgpt_account_id":"partial","organizations":"invalid"}`)) != "" {
		t.Fatal("partially decoded claims accepted")
	}
}

func TestClientRefreshCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	store := testStore(t)
	save(t, store, Credential{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now()})
	client := testClient(t, server, store)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := client.Access(ctx); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("refresh cancellation hung")
	}
	current, err := store.Load(t.Context())
	if err != nil || current.AccessToken != "old" {
		t.Fatal("canceled refresh changed credentials", err)
	}
}

type failingCommitStore struct {
	credential Credential
	failure    error
}

func (s failingCommitStore) Load(context.Context) (Credential, error) { return s.credential, nil }
func (s failingCommitStore) Update(ctx context.Context, change func(*Credential) (*Credential, error)) (*Credential, error) {
	c := s.credential
	if _, err := change(&c); err != nil {
		return nil, err
	}
	return nil, s.failure
}

func (s failingCommitStore) UpdateRefresh(ctx context.Context, change func(*Credential) (*Credential, error)) (*Credential, error) {
	return s.Update(ctx, change)
}

func TestClientRequiresDurableRefreshCommit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"new","refresh_token":"rotated","expires_in":3600}`)
	}))
	defer server.Close()
	failure := errors.New("disk write failed")
	store := failingCommitStore{credential: Credential{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now()}, failure: failure}
	access, err := testClient(t, server, store).Access(t.Context())
	if !errors.Is(err, failure) || access.Token != "" {
		t.Fatal("uncommitted refresh escaped", err)
	}
}

func TestClientPollingDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer server.Close()
	client := testClient(t, server, testStore(t))
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := client.CompleteDeviceAuth(ctx, DeviceAuthorization{DeviceAuthID: "device", UserCode: "code", PollInterval: time.Second})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

type cancelBeforeCommitStore struct {
	*localconfig.FileStore
	cancel      context.CancelFunc
	commitLive  bool
	commitBound time.Duration
}

func (s *cancelBeforeCommitStore) UpdateWithCommit(ctx context.Context, change func(*localconfig.Config) (context.Context, error)) (localconfig.Config, error) {
	return s.FileStore.UpdateWithCommit(ctx, func(config *localconfig.Config) (context.Context, error) {
		commit, err := change(config)
		if err == nil {
			s.cancel()
			s.commitLive = commit.Err() == nil
			deadline, ok := commit.Deadline()
			if ok {
				s.commitBound = time.Until(deadline)
			}
		}
		return commit, err
	})
}

func TestClientCommitsAcceptedRefreshAfterCancellation(t *testing.T) {
	shared, err := localconfig.NewFileStore(filepath.Join(t.TempDir(), "sol", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = shared.Update(t.Context(), func(config *localconfig.Config) error {
		config.Providers["openai"] = localconfig.ProviderAuth{APIKey: &localconfig.APIKeyCredential{Key: "independent-key"}, OAuth: map[string]localconfig.OAuthCredential{"codex": {AccessToken: "old", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Hour)}}}
		config.DefaultModel = &localconfig.ModelSelection{Model: "openai/gpt-5.4", Auth: "codex"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("refresh_token") != "old-refresh" {
			t.Error("unexpected refresh")
		}
		fmt.Fprint(w, `{"access_token":"accepted-access","refresh_token":"rotated-refresh","expires_in":3600}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	hooked := &cancelBeforeCommitStore{FileStore: shared, cancel: cancel}
	credentials, err := NewStore(hooked)
	if err != nil {
		t.Fatal(err)
	}
	access, err := testClient(t, server, credentials).Access(ctx)
	if !errors.Is(err, context.Canceled) || access.Token != "" {
		t.Fatal("canceled caller received access", err)
	}
	if !hooked.commitLive || hooked.commitBound <= 0 || hooked.commitBound > refreshCommitTimeout {
		t.Fatal("refresh commit did not receive independent bounded context")
	}
	config, err := shared.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	credential := config.Providers["openai"].OAuth["codex"]
	if credential.RefreshToken != "rotated-refresh" || credential.AccessToken != "accepted-access" || config.Providers["openai"].APIKey.Key != "independent-key" || config.DefaultModel.Auth != "codex" {
		t.Fatal("accepted rotation or unrelated configuration lost")
	}
}

func TestClientCanceledBeforeRefreshDoesNotExchange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("canceled request refreshed credentials") }))
	defer server.Close()
	store := testStore(t)
	save(t, store, Credential{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now()})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := testClient(t, server, store).Access(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	c, err := store.Load(t.Context())
	if err != nil || c.AccessToken != "old" {
		t.Fatal("canceled request changed credentials", err)
	}
}
