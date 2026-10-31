package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// CommandControl is host-only bash transport configuration, not part of the
// model's BashInput schema. Signals are opaque IDs verified by the host before
// the command context is cancelled. No stdout content is interpreted as control.
type CommandControl struct {
	VerifyURL string `json:"verify_url"`
	Bearer    string `json:"bearer"`
}

type commandControl struct {
	listener net.Listener
	dir      string
	mu       sync.Mutex
	id       string
	failure  string
	done     chan struct{}
}

func startCommandControl(ctx context.Context, config CommandControl, stop context.CancelFunc) (*commandControl, error) {
	u, err := url.Parse(config.VerifyURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || config.Bearer == "" {
		return nil, errors.New("bash: valid host command-control verifier and credential required")
	}
	dir, err := os.MkdirTemp("", "sol-command-control-")
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "control.sock"))
	if err != nil {
		os.Remove(dir)
		return nil, err
	}
	c := &commandControl{listener: listener, dir: dir, done: make(chan struct{})}
	go func() {
		defer close(c.done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			var signal struct {
				ID string `json:"id"`
			}
			err = json.NewDecoder(io.LimitReader(conn, 512)).Decode(&signal)
			accepted := false
			if err == nil && len(signal.ID) == 36 {
				verifyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				req, reqErr := http.NewRequestWithContext(verifyCtx, http.MethodPost, config.VerifyURL+"/"+url.PathEscape(signal.ID)+"/verify", nil)
				if reqErr == nil {
					req.Header.Set("Authorization", "Bearer "+config.Bearer)
					client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
					response, callErr := client.Do(req)
					if callErr == nil {
						accepted = response.StatusCode == http.StatusNoContent
						response.Body.Close()
						if !accepted {
							c.mu.Lock()
							c.failure = "verification_http_" + strconv.Itoa(response.StatusCode)
							c.mu.Unlock()
							stop()
						}
					} else {
						// Transport diagnostics are a separate channel from verified
						// challenge IDs and contain neither credentials nor URLs.
						c.mu.Lock()
						c.failure = "verification_transport"
						c.mu.Unlock()
						stop()
					}
				}
				cancel()
			}
			if accepted {
				c.mu.Lock()
				c.id = signal.ID
				c.mu.Unlock()
				// Cancellation kills the entire process group, even when a shell
				// ignores CLI failure or uses a pipeline. Earlier effects can exist.
				stop()
			}
			_ = json.NewEncoder(conn).Encode(map[string]bool{"accepted": accepted})
			conn.Close()
			c.mu.Lock()
			failed := c.failure != ""
			c.mu.Unlock()
			if accepted || failed {
				return
			}
		}
	}()
	return c, nil
}

func (c *commandControl) close() (string, string) {
	c.listener.Close()
	<-c.done
	os.Remove(filepath.Join(c.dir, "control.sock"))
	os.Remove(c.dir)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.id, c.failure
}
