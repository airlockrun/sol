package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	Issuer       = "https://auth.openai.com"
	ClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	LoginTimeout = 10 * time.Minute
)

// Access contains the current bearer token and its Codex routing metadata.
type Access struct{ Token, AccountID, ComputeResidency string }

// Source supplies credentials explicitly at the request boundary.
type Source interface {
	Access(context.Context) (Access, error)
}

// ClientOptions requires an explicit HTTP client, protocol identity and store.
// HTTP issuer URLs support local test servers; production uses Issuer.
type ClientOptions struct {
	HTTPClient *http.Client
	Issuer     string
	ClientID   string
	UserAgent  string
	// Store must also implement RefreshStore so accepted token rotations can
	// commit independently of caller cancellation.
	Store Store
}

// Client implements device authentication and a refreshable Source.
type Client struct{ opts ClientOptions }

// NewClient validates explicit dependencies without accessing the store.
func NewClient(opts ClientOptions) (*Client, error) {
	u, err := url.Parse(opts.Issuer)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("codex: invalid issuer")
	}
	if opts.HTTPClient == nil || opts.Store == nil || opts.ClientID == "" || opts.UserAgent == "" {
		return nil, errors.New("codex: HTTPClient, Store, ClientID and UserAgent are required")
	}
	if _, ok := opts.Store.(RefreshStore); !ok {
		return nil, errors.New("codex: Store must support durable refresh commits")
	}
	opts.Issuer = strings.TrimRight(opts.Issuer, "/")
	return &Client{opts: opts}, nil
}

// DeviceAuthorization is a transient login attempt, never a persisted credential.
type DeviceAuthorization struct {
	DeviceAuthID    string
	UserCode        string
	VerificationURL string
	PollInterval    time.Duration
}

// StartDeviceAuth creates a device attempt and returns the browser instructions.
func (c *Client) StartDeviceAuth(ctx context.Context) (DeviceAuthorization, error) {
	var data struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		Interval     json.RawMessage `json:"interval"`
	}
	body, _ := json.Marshal(map[string]string{"client_id": c.opts.ClientID})
	_, err := c.request(ctx, "/api/accounts/deviceauth/usercode", "application/json", string(body), &data)
	if err != nil {
		return DeviceAuthorization{}, err
	}
	if data.DeviceAuthID == "" || data.UserCode == "" {
		return DeviceAuthorization{}, errors.New("codex: incomplete device authorization")
	}
	seconds := 5
	if len(data.Interval) != 0 {
		text := strings.Trim(string(data.Interval), `"`)
		n, err := strconv.Atoi(text)
		if err != nil || n < 1 || n > 300 {
			return DeviceAuthorization{}, errors.New("codex: invalid polling interval")
		}
		seconds = n
	}
	return DeviceAuthorization{data.DeviceAuthID, data.UserCode, c.opts.Issuer + "/codex/device", time.Duration(seconds) * time.Second}, nil
}

// CompleteDeviceAuth is bounded even when the caller has no deadline. It returns
// credentials without persisting them; callers explicitly commit successful login.
func (c *Client) CompleteDeviceAuth(ctx context.Context, device DeviceAuthorization) (Credential, error) {
	if device.DeviceAuthID == "" || device.UserCode == "" || device.PollInterval < time.Second || device.PollInterval > 300*time.Second {
		return Credential{}, errors.New("codex: invalid device authorization")
	}
	ctx, cancel := context.WithTimeout(ctx, LoginTimeout)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"device_auth_id": device.DeviceAuthID, "user_code": device.UserCode})
	for {
		var data struct {
			Code     string `json:"authorization_code"`
			Verifier string `json:"code_verifier"`
		}
		status, err := c.request(ctx, "/api/accounts/deviceauth/token", "application/json", string(body), &data)
		if err == nil {
			if data.Code == "" || data.Verifier == "" {
				return Credential{}, errors.New("codex: incomplete device approval")
			}
			return c.exchange(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {data.Code}, "code_verifier": {data.Verifier}, "redirect_uri": {c.opts.Issuer + "/deviceauth/callback"}}, nil)
		}
		if status != 403 && status != 404 {
			return Credential{}, err
		}
		timer := time.NewTimer(device.PollInterval + 3*time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Credential{}, ctx.Err()
		case <-timer.C:
		}
	}
}

// Access reloads credentials under the store's transaction lock. Refresh token
// rotation is committed before another process may refresh or send a request.
func (c *Client) Access(ctx context.Context) (Access, error) {
	if err := ctx.Err(); err != nil {
		return Access{}, err
	}
	credential, err := c.opts.Store.(RefreshStore).UpdateRefresh(ctx, func(current *Credential) (*Credential, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if current == nil {
			return nil, ErrNotLoggedIn
		}
		if err := validateCredential(*current); err != nil {
			return nil, err
		}
		if time.Until(current.ExpiresAt) > time.Minute {
			return current, nil
		}
		next, err := c.exchange(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {current.RefreshToken}}, current)
		if err != nil {
			return nil, err
		}
		return &next, nil
	})
	if err != nil {
		return Access{}, err
	}
	if err := ctx.Err(); err != nil {
		return Access{}, err
	}
	if credential == nil {
		return Access{}, ErrNotLoggedIn
	}
	return Access{credential.AccessToken, credential.AccountID, residency(credential.AccessToken)}, nil
}

func (c *Client) exchange(ctx context.Context, form url.Values, previous *Credential) (Credential, error) {
	form.Set("client_id", c.opts.ClientID)
	var data struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		ID      string `json:"id_token"`
		Expires *int64 `json:"expires_in"`
	}
	_, err := c.request(ctx, "/oauth/token", "application/x-www-form-urlencoded", form.Encode(), &data)
	if err != nil {
		return Credential{}, err
	}
	seconds := int64(3600)
	if data.Expires != nil {
		seconds = *data.Expires
	}
	if seconds <= 0 || seconds > 365*24*3600 {
		return Credential{}, errors.New("codex: invalid token expiration")
	}
	result := Credential{AccessToken: data.Access, RefreshToken: data.Refresh, ExpiresAt: time.Now().Add(time.Duration(seconds) * time.Second), AccountID: accountID(data.ID)}
	if result.AccountID == "" {
		result.AccountID = accountID(data.Access)
	}
	if previous != nil {
		if result.RefreshToken == "" {
			result.RefreshToken = previous.RefreshToken
		}
		if result.AccountID == "" {
			result.AccountID = previous.AccountID
		}
	}
	if err := validateCredential(result); err != nil {
		return Credential{}, err
	}
	return result, nil
}

func (c *Client) request(ctx context.Context, path, contentType, body string, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.opts.Issuer+path, strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", c.opts.UserAgent)
	// Refuse redirects so credentials cannot be forwarded to another endpoint.
	client := *c.opts.HTTPClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("codex: %s failed (HTTP %d)", path, resp.StatusCode)
	}
	d := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := d.Decode(out); err != nil {
		return resp.StatusCode, errors.New("codex: invalid server response")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return resp.StatusCode, errors.New("codex: trailing server response")
	}
	return resp.StatusCode, nil
}

type claims struct {
	Account   string `json:"chatgpt_account_id"`
	Residency string `json:"chatgpt_compute_residency"`
	Auth      *struct {
		Account   string `json:"chatgpt_account_id"`
		Residency string `json:"chatgpt_compute_residency"`
	} `json:"https://api.openai.com/auth"`
	Organizations []struct {
		ID string `json:"id"`
	} `json:"organizations"`
}

func decodeClaims(token string) claims {
	var result claims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return result
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err == nil {
		if err := json.Unmarshal(data, &result); err != nil {
			return claims{}
		}
	}
	return result
}
func accountID(token string) string {
	c := decodeClaims(token)
	if c.Account != "" {
		return c.Account
	}
	if c.Auth != nil && c.Auth.Account != "" {
		return c.Auth.Account
	}
	if len(c.Organizations) > 0 {
		return c.Organizations[0].ID
	}
	return ""
}
func residency(token string) string {
	c := decodeClaims(token)
	value := c.Residency
	if c.Auth != nil && c.Auth.Residency != "" {
		value = c.Auth.Residency
	}
	if value == "no_constraint" {
		return ""
	}
	return value
}
