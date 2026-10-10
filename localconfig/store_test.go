package localconfig

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *FileStore {
	t.Helper()
	s, err := NewFileStore(filepath.Join(t.TempDir(), "sol", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func seed(t *testing.T, s Store) {
	t.Helper()
	_, err := s.Update(t.Context(), func(c *Config) error {
		c.Providers["work/openai"] = ProviderConfig{Auth: "codex", Credentials: &OAuthCredential{AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour), AccountID: "0"}}
		c.Providers["personal/anthropic"] = ProviderConfig{Auth: APIKeyMode, Key: "anthropic-test"}
		c.DefaultModel = "work/openai/gpt-5.4"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFileStore(t *testing.T) {
	s := testStore(t)
	empty, err := s.Load(t.Context())
	if err != nil || len(empty.Providers) != 0 || empty.DefaultModel != "" {
		t.Fatal("invalid empty store", err)
	}
	seed(t, s)
	if runtime.GOOS != "windows" {
		for path, mode := range map[string]os.FileMode{s.path: 0600, filepath.Dir(s.path): 0700, s.path + ".lock": 0600} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != mode {
				t.Fatalf("wrong private permissions: %s", path)
			}
		}
	}
	before, _ := s.Load(t.Context())
	encodedBefore, _ := json.Marshal(before)
	failure := errors.New("callback failed")
	_, err = s.Update(t.Context(), func(c *Config) error {
		c.Providers["work/openai"].Credentials.AccessToken = "changed"
		c.DefaultModel = "personal/anthropic/claude"
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	after, _ := s.Load(t.Context())
	encodedAfter, _ := json.Marshal(after)
	if string(encodedBefore) != string(encodedAfter) {
		t.Fatal("failed callback persisted")
	}
	if _, err := NewFileStore("relative"); err == nil {
		t.Fatal("accepted relative path")
	}
	if _, err := s.Update(t.Context(), nil); err == nil {
		t.Fatal("accepted nil callback")
	}
}

func TestFileStoreVersionOne(t *testing.T) {
	s := testStore(t)
	if _, err := s.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	credential := OAuthCredential{AccessToken: "existing-access", RefreshToken: "existing-refresh", ExpiresAt: time.Now().Add(time.Hour), AccountID: "account"}
	data, err := json.Marshal(struct {
		Version int             `json:"version"`
		Codex   OAuthCredential `json:"codex"`
	}{1, credential})
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(filepath.Dir(s.path), "auth.json")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := s.Load(t.Context())
	if err != nil || c.Providers["codex/openai"].Credentials.RefreshToken != "existing-refresh" {
		t.Fatal("lost version 1 credential", err)
	}
	_, err = s.Update(t.Context(), func(c *Config) error {
		c.Providers["default/anthropic"] = ProviderConfig{Auth: APIKeyMode, Key: "new-key"}
		c.DefaultModel = "default/anthropic/claude"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.Load(t.Context())
	if err != nil || c.Providers["codex/openai"].Credentials.RefreshToken != "existing-refresh" || c.DefaultModel != "default/anthropic/claude" {
		t.Fatal("upgrade lost data", err)
	}
	preserved, err := os.ReadFile(source)
	if err != nil || string(preserved) != string(data) {
		t.Fatal("import changed source")
	}
	data, err = os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record["version"] != float64(Version) || record["codex"] != nil {
		t.Fatal("not a general typed record")
	}
}

func TestFileStoreCorruption(t *testing.T) {
	for _, data := range []string{`invalid`, `{"version":99,"providers":{}}`, `{"version":2}`, `{"version":1,"codex":{}}`, `{} {}`, `{"version":2,"providers":{},"future_setting":"preserve-me"}`} {
		t.Run(data, func(t *testing.T) {
			s := testStore(t)
			s.Load(t.Context())
			if err := os.WriteFile(s.path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Load(t.Context()); err == nil {
				t.Fatal("accepted corrupt or unsupported store")
			}
			if _, err := s.Update(t.Context(), func(*Config) error { t.Fatal("callback should not run"); return nil }); err == nil {
				t.Fatal("overwrote unsupported data")
			}
		})
	}
}

func TestFileStoreLockCancellation(t *testing.T) {
	s := testStore(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := s.Update(t.Context(), func(*Config) error { close(entered); <-release; return nil })
		done <- err
	}()
	<-entered
	other, _ := NewFileStore(s.path)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := other.Load(ctx)
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestFileStoreCrossProcess(t *testing.T) {
	if path := os.Getenv("SOL_LOCALCONFIG_TEST_STORE"); path != "" {
		s, err := NewFileStore(path)
		if err != nil {
			t.Fatal(err)
		}
		for range 10 {
			_, err := s.Update(t.Context(), func(c *Config) error {
				p := c.Providers["work/openai"]
				token := *p.Credentials
				n, err := strconv.Atoi(token.AccountID)
				if err != nil {
					return err
				}
				time.Sleep(time.Millisecond)
				token.AccountID = strconv.Itoa(n + 1)
				p.Credentials = &token
				c.Providers["work/openai"] = p
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	s := testStore(t)
	seed(t, s)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestFileStoreCrossProcess$")
			cmd.Env = append(os.Environ(), "SOL_LOCALCONFIG_TEST_STORE="+s.path)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("subprocess: %v\n%s", err, output)
			}
		})
	}
	wg.Wait()
	c, err := s.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if c.Providers["work/openai"].Credentials.AccountID != "40" || c.Providers["personal/anthropic"].Key != "anthropic-test" || c.DefaultModel != "work/openai/gpt-5.4" {
		t.Fatal("lost cross-process update or unrelated data")
	}
}

func TestFileStoreConcurrentProviderUpdates(t *testing.T) {
	s := testStore(t)
	seed(t, s)
	var wg sync.WaitGroup
	for n := range 20 {
		wg.Go(func() {
			other, _ := NewFileStore(s.path)
			_, err := other.Update(t.Context(), func(c *Config) error {
				c.Providers["account-"+strconv.Itoa(n)+"/openai"] = ProviderConfig{Auth: APIKeyMode, Key: "test-key"}
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	c, err := s.Load(t.Context())
	if err != nil || len(c.Providers) != 22 || c.DefaultModel != "work/openai/gpt-5.4" || c.Providers["work/openai"].Credentials.RefreshToken != "refresh" {
		t.Fatal("lost concurrent providers or default", err)
	}
}

func TestFileStoreRejectsUnsafeFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions and symlinks")
	}
	for _, kind := range []string{"permissions", "credential symlink", "lock symlink", "directory symlink"} {
		t.Run(kind, func(t *testing.T) {
			s := testStore(t)
			seed(t, s)
			switch kind {
			case "permissions":
				os.Chmod(s.path, 0644)
			case "credential symlink":
				os.Rename(s.path, s.path+".target")
				os.Symlink(s.path+".target", s.path)
			case "lock symlink":
				os.Remove(s.path + ".lock")
				os.Symlink(s.path, s.path+".lock")
			case "directory symlink":
				dir := filepath.Dir(s.path)
				os.Rename(dir, dir+".target")
				os.Symlink(dir+".target", dir)
			}
			if _, err := s.Load(t.Context()); err == nil {
				t.Fatal("accepted unsafe store")
			}
		})
	}
}

func TestConfigValidation(t *testing.T) {
	for _, c := range []Config{{}, {Providers: map[string]ProviderConfig{"bad/id": {}}}, {Providers: map[string]ProviderConfig{"work/openai": {Auth: APIKeyMode, Key: "key", KeyEnv: "KEY"}}}, {Providers: map[string]ProviderConfig{"work/openai": {Auth: "codex", Credentials: &OAuthCredential{}}}}, {Providers: map[string]ProviderConfig{}, DefaultModel: "gpt-4o"}} {
		if err := c.Validate(); err == nil {
			t.Fatal("invalid record accepted")
		}
	}
}

func TestFileStoreRejectsOversizedUpdate(t *testing.T) {
	s := testStore(t)
	seed(t, s)
	_, err := s.Update(t.Context(), func(c *Config) error {
		c.Providers["too-large/openai"] = ProviderConfig{Auth: APIKeyMode, Key: strings.Repeat("x", maxStoreBytes)}
		return nil
	})
	if err == nil {
		t.Fatal("wrote an unreadable configuration")
	}
	c, err := s.Load(t.Context())
	if err != nil || len(c.Providers) != 2 {
		t.Fatal("oversized update changed store", err)
	}
}

func TestFileStoreRejectsOversizedRead(t *testing.T) {
	s := testStore(t)
	if _, err := s.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	data := `{"version":3,"providers":{}}` + strings.Repeat(" ", maxStoreBytes)
	if err := os.WriteFile(s.path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(t.Context()); err == nil {
		t.Fatal("truncated oversized input accepted")
	}
}

func TestFileStoreCanceledConfigurationUpdateIsNotCommitted(t *testing.T) {
	s := testStore(t)
	seed(t, s)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err := s.Update(ctx, func(config *Config) error {
		config.DefaultModel = "personal/anthropic/claude"
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	config, err := s.Load(t.Context())
	if err != nil || config.DefaultModel != "work/openai/gpt-5.4" {
		t.Fatal("canceled ordinary configuration change was committed", err)
	}
}

func TestFileStoreCommitContextContract(t *testing.T) {
	for _, kind := range []string{"nil", "unbounded", "expired"} {
		t.Run(kind, func(t *testing.T) {
			s := testStore(t)
			seed(t, s)
			_, err := s.UpdateWithCommit(t.Context(), func(config *Config) (context.Context, error) {
				config.DefaultModel = ""
				switch kind {
				case "nil":
					return nil, nil
				case "unbounded":
					return context.Background(), nil
				default:
					ctx, cancel := context.WithTimeout(context.Background(), -time.Second)
					defer cancel()
					return ctx, nil
				}
			})
			if err == nil {
				t.Fatal("accepted invalid commit context")
			}
			config, err := s.Load(t.Context())
			if err != nil || config.DefaultModel == "" {
				t.Fatal("invalid commit changed store", err)
			}
		})
	}
}
