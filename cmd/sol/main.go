// Sol CLI - Minimal thinking loop in Go (matching OpenCode behavior)
//
// Usage:
//
//	sol [options] <prompt>
//
// Options:
//
//	-model string     Model to use (default "gpt-4o")
//	-agent string     Agent type: build, plan, explore, general (default "build")
//	-session string   Session ID for prompt caching (default: auto-generated)
//	-h                Show help
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"

	"github.com/airlockrun/goai"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/sol"
	"github.com/airlockrun/sol/agent"
	"github.com/airlockrun/sol/auth/codex"
	"github.com/airlockrun/sol/bus"
	"github.com/airlockrun/sol/localconfig"
	"github.com/airlockrun/sol/provider"
	"github.com/airlockrun/sol/tools"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if len(os.Args) > 1 && (os.Args[1] == "auth" || os.Args[1] == "config") {
		store, err := localStore()
		if err == nil && os.Args[1] == "config" {
			err = runConfig(ctx, os.Args[2:], os.Stdout, store)
		} else if err == nil {
			var client *codex.Client
			client, err = localCodex(store)
			if err == nil {
				err = runAuth(ctx, os.Args[2:], os.Stdout, client, store, func(ctx context.Context, stdin bool) (string, error) {
					return readAPIKey(ctx, os.Stdin, os.Stderr, stdin)
				})
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "[sol]", err)
			os.Exit(1)
		}
		return
	}
	// Flags
	authFlag := flag.String("auth", "", "Authentication: api-key or codex; explicit model overrides default auth to api-key")
	modelFlag := flag.String("model", "", "Model to use (provider/model); uses saved default or openai/gpt-4o")
	agentFlag := flag.String("agent", "build", "Agent type: build, plan, explore, general")
	nameFlag := flag.String("name", "", "Agent name for prompts (default: agent type name)")
	noTitleFlag := flag.Bool("notitle", false, "Disable title generation (for replay testing)")
	titleModelFlag := flag.String("title-model", "", "Model for title generation (default: selected main model)")
	mcpFlag := flag.String("mcp", "", "MCP servers (comma-separated name=url pairs, e.g., 'docs=http://localhost:8080/mcp')")
	searchFlag := flag.Bool("search", false, "Enable web search (native provider key, or independent Brave/Perplexity key from env/store)")
	interactiveFlag := flag.Bool("i", false, "Interactive mode - prompt for permissions (default: auto-approve)")
	helpFlag := flag.Bool("h", false, "Show help")
	flag.Parse()
	explicitModel, explicitTitle, explicitAuth := false, false, false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "model" {
			explicitModel = true
		}
		if f.Name == "title-model" {
			explicitTitle = true
		}
		if f.Name == "auth" {
			explicitAuth = true
		}
	})

	// Determine prompts from CLI args
	var prompts []string
	if flag.NArg() > 0 {
		prompts = []string{strings.Join(flag.Args(), " ")}
	}

	if *helpFlag || len(prompts) == 0 {
		fmt.Println(`sol - Minimal thinking loop in Go (matching OpenCode behavior)

Usage:
  sol [options] <prompt>
  sol auth login|status|logout codex
  sol auth set-key PROVIDER [--stdin]
  sol auth status|remove-key PROVIDER
  sol config model [--auth api-key|codex] [PROVIDER/MODEL]
  sol config model --clear

Options:
  -auth string      Authentication: api-key or codex (uses saved model/auth pair)
  -model string     Model override; uses api-key unless -auth is also supplied
  -agent string     Agent type: build, plan, explore, general (default "build")
  -name string      Agent name for prompts (default: agent type name)
  -mcp string       MCP servers (comma-separated name=url pairs)
  -search           Enable web search tool
  -notitle          Disable title generation (for replay testing)
  -title-model      Title model override (default: selected main model)
  -i                Interactive mode - prompt for permissions (default: auto-approve)
  -h                Show help

Agent Types:
  build    Full-access agent for software engineering tasks (default)
  plan     Read-only agent for planning and design
  explore  Fast agent for codebase exploration
  general  General-purpose subagent for delegated tasks

Examples:
  sol "Hello world"
  sol -model openai/gpt-4o-mini "Create a hello.py file"
  sol -model anthropic/claude-3-5-sonnet "Explain this code"
  sol -agent plan "Design a user authentication system"
  sol -name opencode -model openai/gpt-4o-mini "List files"`)
		if *helpFlag {
			os.Exit(0)
		}
		os.Exit(1)
	}

	// Hosted proxy requests do not discover local credentials or inherit local
	// model/auth defaults. Configuration commands above remain explicitly local.
	proxyURL := os.Getenv("AIRLOCK_API_URL")
	proxyToken := os.Getenv("AIRLOCK_BUILD_TOKEN")
	var store localconfig.Store
	config := localconfig.Config{Providers: make(map[string]localconfig.ProviderAuth)}
	var err error
	if proxyURL == "" {
		store, err = localStore()
		if err == nil {
			config, err = store.Load(ctx)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "[sol]", err)
			os.Exit(1)
		}
	} else if explicitAuth && *authFlag == "codex" {
		fmt.Fprintln(os.Stderr, "[sol] -auth codex requires direct local mode; unset AIRLOCK_API_URL")
		os.Exit(1)
	}
	selection, err := selectModel(config, *modelFlag, *authFlag, explicitModel, explicitAuth)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[sol]", err)
		os.Exit(1)
	}
	*modelFlag = selection.Model
	*authFlag = selection.Auth

	// Parse model to determine provider
	providerID, modelID := provider.ParseModel(*modelFlag)
	baseURL := os.Getenv("OPENAI_BASE_URL")
	if err := validateAuthMode(*authFlag, providerID, modelID, proxyURL, baseURL, explicitModel || config.DefaultModel != nil); err != nil {
		fmt.Fprintln(os.Stderr, "[sol]", err)
		os.Exit(1)
	}
	var directModel, titleModel stream.Model
	if *authFlag == "codex" {
		client, err := localCodex(store)
		if err != nil {
			fmt.Fprintln(os.Stderr, "[sol]", err)
			os.Exit(1)
		}
		opts := provider.CodexOptions{Credentials: client, HTTPClient: &http.Client{}, URL: provider.CodexURL, UserAgent: "sol/" + sol.Version, SessionID: rand.Text()}
		directModel, titleModel, err = codexModels(modelID, *titleModelFlag, explicitTitle, opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, "[sol]", err)
			os.Exit(1)
		}
	}

	// Load API key based on provider (not needed in proxy mode)
	var apiKey string
	if proxyURL == "" && *authFlag == "api-key" {
		apiKey, err = provider.ResolveLocalAPIKey(config, providerID, os.LookupEnv)
		if err != nil {
			fmt.Fprintln(os.Stderr, "[sol]", err)
			os.Exit(1)
		}
		directModel = provider.CreateModel(providerID, modelID, provider.Options{APIKey: apiKey, BaseURL: baseURL})
		titleModel = directModel
		if explicitTitle && !*noTitleFlag {
			id, model := provider.ParseModel(*titleModelFlag)
			key, err := provider.ResolveLocalAPIKey(config, id, os.LookupEnv)
			if err != nil {
				fmt.Fprintln(os.Stderr, "[sol] title:", err)
				os.Exit(1)
			}
			titleModel = provider.CreateModel(id, model, provider.Options{APIKey: key, BaseURL: baseURL})
		}
	}

	// Get agent from registry (factory creates it with right tools for this model)
	selectedAgent, exists := agent.Get(*agentFlag, modelID)
	if !exists {
		fmt.Fprintf(os.Stderr, "[sol] Error: Unknown agent type: %s\n", *agentFlag)
		fmt.Fprintf(os.Stderr, "Available agents: %v\n", agent.List())
		os.Exit(1)
	}

	// Set the full model string and optional name override
	selectedAgent.Model = *modelFlag
	if *nameFlag != "" {
		selectedAgent.Name = *nameFlag
	}

	// Enable web search if requested
	if *searchFlag {
		t, ok := resolveSearch(*authFlag, providerID, apiKey, config)
		if !ok {
			fmt.Fprintf(os.Stderr, "[sol] Error: -search requires a search-capable provider or an independent Brave/Perplexity API key in env/store\n")
			os.Exit(1)
		}
		selectedAgent.Tools.Add(t)
		fmt.Printf("[%s] Web search: enabled\n", selectedAgent.Name)
	}

	// Connect to MCP servers if configured
	if *mcpFlag != "" {
		servers := parseMCPFlag(*mcpFlag)
		mcpClient, mcpTools, err := sol.ConnectMCPServers(ctx, servers)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[sol] MCP error: %v\n", err)
			os.Exit(1)
		}
		defer mcpClient.DisconnectAll()
		selectedAgent.Tools = tools.MergeToolSets(selectedAgent.Tools, mcpTools)
		fmt.Printf("[%s] MCP: %d server(s), %d tool(s)\n", selectedAgent.Name, len(servers), len(mcpTools))
	}

	cwd, _ := os.Getwd()

	// Build the proxy model if in proxy mode.
	proxyModel := directModel
	if proxyURL != "" {
		proxyModel = provider.CreateProxyModel(*modelFlag, provider.ProxyOptions{
			BaseURL: proxyURL,
			Token:   proxyToken,
		})
	}
	if titleModel == nil {
		titleModel = proxyModel
	}

	fmt.Printf("[%s] Starting...\n", selectedAgent.Name)
	if len(prompts) == 1 {
		fmt.Printf("[%s] Prompt: %s\n", selectedAgent.Name, prompts[0])
	} else {
		fmt.Printf("[%s] Test mode: %d prompts\n", selectedAgent.Name, len(prompts))
	}
	fmt.Printf("[%s] Model: %s (auth: %s; using %s prompt)\n", selectedAgent.Name, *modelFlag, *authFlag, sol.GetPromptForModel(modelID))

	if proxyURL != "" {
		fmt.Printf("[%s] Using Airlock proxy: %s\n", selectedAgent.Name, proxyURL)
	} else if baseURL != "" {
		fmt.Printf("[%s] Using base URL: %s\n", selectedAgent.Name, baseURL)
	}

	// Build permission rules
	var rules []bus.PermissionRule
	if !*interactiveFlag {
		rules = []bus.PermissionRule{{Permission: "*", Pattern: "*", Action: "allow"}}
	}

	// Channel for title generation result (async) - only for single prompt mode
	enableTitleGen := !*noTitleFlag
	var titleChan <-chan sol.TitleResult
	if enableTitleGen && len(prompts) == 1 {
		titleChan = sol.GenerateTitleWithModelAsync(ctx, prompts[0], titleModel)
	}

	var totalSteps int
	var messages []goai.Message // nil for first run

	for i, prompt := range prompts {
		if len(prompts) > 1 {
			fmt.Printf("\n[%s] === Prompt %d/%d: %s ===\n", selectedAgent.Name, i+1, len(prompts), prompt)
		}

		for {
			opts := sol.RunnerOptions{
				Agent:   selectedAgent,
				APIKey:  apiKey,
				BaseURL: baseURL,
				WorkDir: cwd,
				Quiet:   false,
				Model:   proxyModel,
			}
			if messages != nil {
				opts.InitialMessages = messages
			}

			runner := sol.NewRunner(opts)
			runner.PermissionManager().SetRules(rules)

			if !*interactiveFlag {
				runner.QuestionManager().SetAutoAnswer(true)
			}

			// Run the agent with context values for tool execution
			runCtx := context.WithValue(ctx, tools.RunnerKey, runner)
			runCtx = context.WithValue(runCtx, tools.WorkDirKey, cwd)

			result, runErr := runner.Run(runCtx, prompt)
			if runErr != nil {
				fmt.Fprintf(os.Stderr, "[%s] Error: %s\n", selectedAgent.Name, runErr)
				os.Exit(1)
			}

			totalSteps += len(result.Steps)
			messages = result.Messages

			if result.Status == sol.RunCompleted {
				break // done with this prompt
			}

			if result.Status == sol.RunSuspended { // Interactive: handle suspension
				if !*interactiveFlag {
					fmt.Fprintf(os.Stderr, "[%s] Unexpected suspension in auto-approve mode\n", selectedAgent.Name)
					os.Exit(1)
				}

				sc := result.SuspensionContext
				fmt.Printf("\n[%s] Suspended: %s\n", selectedAgent.Name, sc.Reason)
				if sc.Reason == "permission" {
					for sc != nil {
						if len(sc.PendingToolCalls) == 0 {
							fmt.Fprintf(os.Stderr, "[%s] Permission suspension has no pending tool call\n", selectedAgent.Name)
							os.Exit(1)
						}
						tc := sc.PendingToolCalls[0]
						fmt.Printf("[%s] Pending tool: %s (call %s)\n", selectedAgent.Name, tc.Name, tc.ID)
						fmt.Printf("[%s] Allow? [y]es / [a]lways / [n]o: ", selectedAgent.Name)

						scanner := bufio.NewScanner(os.Stdin)
						if !scanner.Scan() {
							fmt.Fprintf(os.Stderr, "[%s] Failed to read input\n", selectedAgent.Name)
							os.Exit(1)
						}

						response := strings.ToLower(strings.TrimSpace(scanner.Text()))
						approved := response != "n" && response != "no"
						if response == "a" || response == "always" {
							rule := bus.PermissionRule{Permission: "*", Pattern: "*", Action: "allow"}
							rules = append(rules, rule)
							runner.PermissionManager().AddRule(rule)
						}

						resolution, resolveErr := runner.ResolvePermissionSuspension(runCtx, sc, approved)
						if resolveErr != nil {
							fmt.Fprintf(os.Stderr, "[%s] Failed to resolve permission: %s\n", selectedAgent.Name, resolveErr)
							os.Exit(1)
						}
						messages = append(messages, resolution.Messages...)
						sc = resolution.SuspensionContext
					}

					prompt = ""
					continue
				}

				for _, tc := range sc.PendingToolCalls {
					fmt.Printf("[%s] Pending tool: %s (call %s)\n", selectedAgent.Name, tc.Name, tc.ID)
					fmt.Printf("[%s] Allow? [y]es / [a]lways / [n]o: ", selectedAgent.Name)

					scanner := bufio.NewScanner(os.Stdin)
					if !scanner.Scan() {
						fmt.Fprintf(os.Stderr, "[%s] Failed to read input\n", selectedAgent.Name)
						os.Exit(1)
					}

					response := strings.ToLower(strings.TrimSpace(scanner.Text()))
					switch response {
					case "y", "yes", "":
						// Allow once — tool will re-execute on next loop
					case "a", "always":
						rules = append(rules, bus.PermissionRule{
							Permission: "*", Pattern: "*", Action: "allow",
						})
					case "n", "no":
						messages = append(messages, goai.NewToolResultDenied(
							tc.ID, tc.Name, "permission denied by user",
						))
					default:
						// Treat as allow once
					}
				}

				prompt = "" // resume with no new prompt
				continue    // re-enter loop with updated messages + rules
			}

			// Any other status (failed, cancelled) — exit
			fmt.Fprintf(os.Stderr, "[%s] Run ended with status: %s\n", selectedAgent.Name, result.Status)
			os.Exit(1)
		}
	}

	// Wait for title generation to complete
	if titleChan != nil {
		if titleResult := <-titleChan; titleResult.Error == nil && titleResult.Title != "" {
			fmt.Printf("[%s] Title: %s\n", selectedAgent.Name, titleResult.Title)
		}
	}

	fmt.Printf("\n[%s] Done (%d steps)\n", selectedAgent.Name, totalSteps)
}

// parseMCPFlag parses the -mcp flag value into MCPServer entries.
// Format: "name=url,name2=url2"
func parseMCPFlag(value string) []sol.MCPServer {
	var servers []sol.MCPServer
	for _, pair := range strings.Split(value, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		name, url, ok := strings.Cut(pair, "=")
		if !ok {
			fmt.Fprintf(os.Stderr, "[sol] Warning: invalid MCP server spec %q (expected name=url)\n", pair)
			continue
		}
		servers = append(servers, sol.MCPServer{
			Name: strings.TrimSpace(name),
			URL:  strings.TrimSpace(url),
		})
	}
	return servers
}
