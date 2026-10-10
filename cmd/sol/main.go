// Sol executes a single batch request against an explicitly selected local session.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/airlockrun/goai"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/sol"
	"github.com/airlockrun/sol/agent"
	"github.com/airlockrun/sol/bus"
	"github.com/airlockrun/sol/localconfig"
	"github.com/airlockrun/sol/provider"
	"github.com/airlockrun/sol/session"
	"github.com/airlockrun/sol/tools"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := runCLI(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "sol:", err)
		os.Exit(1)
	}
}

func runCLI(ctx context.Context) error {
	if len(os.Args) > 1 && (os.Args[1] == "auth" || os.Args[1] == "config") {
		store, err := localStore()
		if err != nil {
			return err
		}
		if os.Args[1] == "config" {
			return runConfig(ctx, os.Args[2:], os.Stdout, store)
		}
		return runAuth(ctx, os.Args[2:], os.Stdout, nil, store, func(ctx context.Context, stdin bool) (string, error) {
			return readAPIKey(ctx, os.Stdin, os.Stderr, stdin)
		})
	}
	if len(os.Args) > 1 && os.Args[1] == "session" {
		return runSessionCommand(ctx, os.Args[2:])
	}
	model := flag.String("model", "", "Local model reference slug/provider/model")
	agentName := flag.String("agent", "", "Agent override (uses session or configured default)")
	name := flag.String("name", "", "Agent name for prompts")
	id := flag.String("session", "", "Existing session ID; overrides SOL_SESSION")
	mcp := flag.String("mcp", "", "MCP servers: name=url,name2=url2")
	search := flag.Bool("search", false, "Enable web search")
	searchEntry := flag.String("search-provider", "", "Named API-key account for independent search")
	verbose := flag.Bool("verbose", false, "Print tool progress to stderr")
	help := flag.Bool("h", false, "Show help")
	version := flag.Bool("version", false, "Show version")
	flag.Parse()
	var emptySelector bool
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "session" && *id == "" {
			emptySelector = true
		}
	})
	if emptySelector {
		return errors.New("-session requires a nonempty existing session ID")
	}
	if *version {
		fmt.Fprintln(os.Stdout, sol.Version)
		return nil
	}
	if *help || flag.NArg() == 0 {
		fmt.Fprintln(os.Stdout, `Usage:
  sol [options] 'request'
  sol config agent [build|plan|explore|general|--clear]
  sol config model [slug/provider/model|--clear]
  sol auth login ENTRY [--method codex]
  sol auth set-key ENTRY [--stdin]
  sol auth status [ENTRY]
  sol auth logout|remove-key ENTRY
  sol session new
  sol session list
  sol session show ID
  sol session compact [ID]

Batch requests print assistant text to stdout. -verbose prints tool progress to stderr.
Select an existing session with -session ID or SOL_SESSION. With neither, each request
creates a new session. No global current session is stored. Sessions retain their
model, agent and working directory; explicit -model and -agent override selections.

Example:
  export SOL_SESSION="$(sol session new)"
  sol 'Inspect this project'
  sol 'Continue with the change'
  sol session compact "$SOL_SESSION"

Options:`)
		flag.CommandLine.SetOutput(os.Stdout)
		flag.PrintDefaults()
		if *help {
			return nil
		}
		return errors.New("a request is required")
	}
	return executeBatch(ctx, strings.Join(flag.Args(), " "), *id, *model, *agentName, *name, *mcp, *search, *searchEntry, *verbose, false)
}

func nestedDepth() (int, error) {
	value, present := os.LookupEnv("SOL_DEPTH")
	if !present {
		return 0, nil
	}
	depth, err := strconv.Atoi(value)
	if err != nil || depth < 0 {
		return 0, errors.New("SOL_DEPTH must be a nonnegative integer")
	}
	if depth > 1 {
		return 0, errors.New("nested Sol limit reached: a child Sol cannot launch another model execution")
	}
	return depth, nil
}

func childEnvironment(depth int) []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "SOL_DEPTH" && key != "SOL_SESSION" {
			env = append(env, entry)
		}
	}
	return append(env, "SOL_DEPTH="+strconv.Itoa(depth+1))
}

func selectedSession(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if value, present := os.LookupEnv("SOL_SESSION"); present {
		if value == "" {
			return "", errors.New("SOL_SESSION is empty; unset it or select an existing session")
		}
		return value, nil
	}
	return "", nil
}

func executeBatch(ctx context.Context, prompt, id, modelRef, agentName, name, mcp string, search bool, searchEntry string, verbose, compact bool) error {
	depth, err := nestedDepth()
	if err != nil {
		return err
	}
	id, err = selectedSession(id)
	if err != nil {
		return err
	}
	root, err := localconfig.SessionPath()
	if err != nil {
		return err
	}
	if compact && id == "" {
		return errors.New("compact requires a session ID or SOL_SESSION")
	}
	newSession := id == ""
	s, err := localconfig.OpenSession(ctx, root, id, newSession)
	if err != nil {
		return err
	}
	defer s.Close()
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if cwd != s.Record.WorkDir {
		return fmt.Errorf("session belongs to %s; run Sol from that directory", s.Record.WorkDir)
	}
	proxyURL := os.Getenv("AIRLOCK_API_URL")
	var store localconfig.Store
	config := localconfig.Config{Providers: make(map[string]localconfig.ProviderConfig)}
	if proxyURL == "" {
		store, err = localStore()
		if err == nil {
			config, err = store.Load(ctx)
		}
		if err != nil {
			return err
		}
	}
	if agentName == "" {
		agentName = s.Record.Agent
	}
	if agentName == "" {
		agentName = config.DefaultAgent
	}
	if agentName == "" {
		agentName = "build"
	}
	if modelRef == "" {
		modelRef = s.Record.Model
	}
	var model stream.Model
	var limits session.ModelLimits
	var apiKey, providerID, modelID, auth string
	baseURL := os.Getenv("OPENAI_BASE_URL")
	if proxyURL == "" {
		ref, binding, err := selectModel(config, modelRef)
		if err != nil {
			return err
		}
		modelRef = ref.Entry + "/" + ref.Model
		providerID, modelID, auth = ref.Provider, ref.Model, binding.Auth
		model, limits, err = provider.ResolveLocalModel(ctx, provider.LocalModelOptions{Model: modelRef, Store: store, HTTPClient: &http.Client{}, UserAgent: "sol/" + sol.Version, SessionID: s.Record.ID, BaseURL: baseURL})
		if err != nil {
			return err
		}
		if auth == localconfig.APIKeyMode {
			apiKey, err = provider.ResolveLocalAPIKey(config, ref.Entry, os.LookupEnv)
			if err != nil {
				return err
			}
		}
	} else {
		if modelRef == "" {
			modelRef = "openai/gpt-4o"
		}
		providerID, modelID = provider.ParseModel(modelRef)
		model = provider.CreateProxyModel(modelRef, provider.ProxyOptions{BaseURL: proxyURL, Token: os.Getenv("AIRLOCK_BUILD_TOKEN")})
	}
	a, exists := agent.Get(agentName, modelID)
	if !exists {
		return fmt.Errorf("unknown agent %q; available: %v", agentName, agent.List())
	}
	// The runner receives the transport identity, not the local account namespace.
	a.Model = providerID + "/" + modelID
	if name != "" {
		a.Name = name
	}
	if search {
		searchAuth, searchID, searchKey := auth, providerID, apiKey
		if searchEntry != "" {
			if proxyURL != "" {
				return errors.New("named search accounts require direct local mode")
			}
			_, searchID, err = localconfig.ParseEntry(searchEntry)
			if err == nil {
				searchKey, err = provider.ResolveLocalAPIKey(config, searchEntry, os.LookupEnv)
			}
			if err != nil {
				return err
			}
			searchAuth = localconfig.APIKeyMode
		}
		t, ok := resolveSearch(searchAuth, searchID, searchKey)
		if !ok {
			return errors.New("-search requires a search-capable API-key account; use -search-provider slug/provider")
		}
		a.Tools.Add(t)
	}
	if mcp != "" {
		servers, err := parseMCPFlag(mcp)
		if err != nil {
			return err
		}
		client, mcpTools, err := sol.ConnectMCPServers(ctx, servers)
		if err != nil {
			return err
		}
		defer client.DisconnectAll()
		a.Tools = tools.MergeToolSets(a.Tools, mcpTools)
	}
	if s.Record.Active && compact {
		return errors.New("session was interrupted; send an explicit request before compacting")
	}
	if s.Record.Active {
		warning := session.FromGoAIMessage(goai.NewUserMessage("The previous local execution was interrupted. Tool effects after the last saved step have unknown outcomes. Inspect the workspace before retrying any action; do not automatically replay prior work."))
		if err := s.Append(ctx, []session.Message{warning}); err != nil {
			return err
		}
	}
	s.Record.Model, s.Record.Agent = modelRef, agentName
	if s.Record.Title == "" && prompt != "" {
		title := []rune(strings.Join(strings.Fields(prompt), " "))
		s.Record.Title = string(title[:min(80, len(title))])
	}
	runner := sol.NewRunner(sol.RunnerOptions{Agent: a, WorkDir: cwd, Quiet: true, Model: model, ModelLimits: limits, SessionStore: s})
	runner.PermissionManager().SetRules([]bus.PermissionRule{{Permission: "*", Pattern: "*", Action: "allow"}})
	runner.QuestionManager().SetAutoAnswer(true)
	runCtx := context.WithValue(ctx, tools.RunnerKey, runner)
	runCtx = context.WithValue(runCtx, tools.WorkDirKey, cwd)
	runCtx = tools.WithProcessEnvironment(runCtx, childEnvironment(depth))
	if compact {
		result, err := runner.Compact(runCtx)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Compacted %s (%d estimated tokens freed).\n", s.Record.ID, result.TokensFreed)
		return nil
	}
	s.Record.Active = true
	if err := s.Save(ctx); err != nil {
		return err
	}
	if newSession {
		fmt.Fprintln(os.Stderr, "Session:", s.Record.ID)
	}
	var outputMu sync.Mutex
	unsubscribe := runner.Bus().SubscribeAll(func(e bus.Event) {
		outputMu.Lock()
		defer outputMu.Unlock()
		switch e.Type {
		case bus.StreamToolCall:
			if verbose {
				fmt.Fprintln(os.Stderr, "Tool:", e.Properties.(stream.ToolCallEvent).ToolName)
			}
		}
	})
	defer unsubscribe()
	result, runErr := runner.Run(runCtx, prompt)
	if result != nil && result.TotalText != "" {
		fmt.Fprintln(os.Stdout, result.TotalText)
	}
	// Keep the interruption marker on failure; the next explicit turn receives it.
	if runErr != nil {
		return runErr
	}
	if result.Status != sol.RunCompleted {
		return fmt.Errorf("run ended with status %s", result.Status)
	}
	s.Record.Active = false
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	return s.Save(commitCtx)
}

func parseMCPFlag(value string) ([]sol.MCPServer, error) {
	var servers []sol.MCPServer
	for _, pair := range strings.Split(value, ",") {
		name, url, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(url) == "" {
			return nil, fmt.Errorf("invalid MCP server %q: expected name=url", pair)
		}
		servers = append(servers, sol.MCPServer{Name: strings.TrimSpace(name), URL: strings.TrimSpace(url)})
	}
	return servers, nil
}
