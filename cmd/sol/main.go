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
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/airlockrun/goai"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/sol"
	"github.com/airlockrun/sol/agent"
	"github.com/airlockrun/sol/bus"
	"github.com/airlockrun/sol/provider"
	"github.com/airlockrun/sol/tools"
)

func main() {
	// Flags
	modelFlag := flag.String("model", "openai/gpt-4o", "Model to use (provider/model format, e.g., openai/gpt-4o-mini)")
	agentFlag := flag.String("agent", "build", "Agent type: build, plan, explore, general")
	nameFlag := flag.String("name", "", "Agent name for prompts (default: agent type name)")
	noTitleFlag := flag.Bool("notitle", false, "Disable title generation (for replay testing)")
	titleModelFlag := flag.String("title-model", "gpt-5-nano", "Model for title generation")
	mcpFlag := flag.String("mcp", "", "MCP servers (comma-separated name=url pairs, e.g., 'docs=http://localhost:8080/mcp')")
	searchFlag := flag.Bool("search", false, "Enable web search tool (uses LLM provider key if capable, or BRAVE_API_KEY/PERPLEXITY_API_KEY)")
	interactiveFlag := flag.Bool("i", false, "Interactive mode - prompt for permissions (default: auto-approve)")
	helpFlag := flag.Bool("h", false, "Show help")
	flag.Parse()

	// Determine prompts from CLI args
	var prompts []string
	if flag.NArg() > 0 {
		prompts = []string{strings.Join(flag.Args(), " ")}
	}

	if *helpFlag || len(prompts) == 0 {
		fmt.Println(`sol - Minimal thinking loop in Go (matching OpenCode behavior)

Usage:
  sol [options] <prompt>

Options:
  -model string     Model to use in provider/model format (default "openai/gpt-4o")
  -agent string     Agent type: build, plan, explore, general (default "build")
  -name string      Agent name for prompts (default: agent type name)
  -mcp string       MCP servers (comma-separated name=url pairs)
  -search           Enable web search tool
  -notitle          Disable title generation (for replay testing)
  -title-model      Model for title generation (default "gpt-5-nano")
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

	// Load .env files first
	loadEnvFile(".env")
	loadEnvFile("../.env")

	// Check for Airlock proxy mode (AIRLOCK_API_URL + AIRLOCK_BUILD_TOKEN).
	// When set, Sol proxies all LLM calls through Airlock instead of using direct API keys.
	proxyURL := os.Getenv("AIRLOCK_API_URL")
	proxyToken := os.Getenv("AIRLOCK_BUILD_TOKEN")

	// Parse model to determine provider
	providerID, modelID := provider.ParseModel(*modelFlag)

	// Load API key based on provider (not needed in proxy mode)
	var apiKey string
	if proxyURL == "" {
		envVarName := provider.GetEnvVarName(providerID)
		apiKey = os.Getenv(envVarName)
		if apiKey == "" {
			fmt.Fprintf(os.Stderr, "[sol] Error: %s not set\n", envVarName)
			os.Exit(1)
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
		t, ok := tools.WebSearch(providerID, apiKey)
		if !ok {
			fmt.Fprintf(os.Stderr, "[sol] Error: -search requires a search-capable provider or BRAVE_API_KEY/PERPLEXITY_API_KEY\n")
			os.Exit(1)
		}
		selectedAgent.Tools.Add(t)
		fmt.Printf("[%s] Web search: enabled\n", selectedAgent.Name)
	}

	// Connect to MCP servers if configured
	if *mcpFlag != "" {
		servers := parseMCPFlag(*mcpFlag)
		mcpClient, mcpTools, err := sol.ConnectMCPServers(context.Background(), servers)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[sol] MCP error: %v\n", err)
			os.Exit(1)
		}
		defer mcpClient.DisconnectAll()
		selectedAgent.Tools = tools.MergeToolSets(selectedAgent.Tools, mcpTools)
		fmt.Printf("[%s] MCP: %d server(s), %d tool(s)\n", selectedAgent.Name, len(servers), len(mcpTools))
	}

	cwd, _ := os.Getwd()
	baseURL := os.Getenv("OPENAI_BASE_URL")

	// Build the proxy model if in proxy mode.
	var proxyModel stream.Model
	if proxyURL != "" {
		proxyModel = provider.CreateProxyModel(*modelFlag, provider.ProxyOptions{
			BaseURL: proxyURL,
			Token:   proxyToken,
		})
	}

	fmt.Printf("[%s] Starting...\n", selectedAgent.Name)
	if len(prompts) == 1 {
		fmt.Printf("[%s] Prompt: %s\n", selectedAgent.Name, prompts[0])
	} else {
		fmt.Printf("[%s] Test mode: %d prompts\n", selectedAgent.Name, len(prompts))
	}
	fmt.Printf("[%s] Model: %s (using %s prompt)\n", selectedAgent.Name, *modelFlag, sol.GetPromptForModel(modelID))

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
		ctx := context.Background()
		titleChan = sol.GenerateTitleAsync(ctx, prompts[0], *titleModelFlag, apiKey, baseURL)
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
			ctx := context.WithValue(context.Background(), tools.RunnerKey, runner)
			ctx = context.WithValue(ctx, tools.WorkDirKey, cwd)

			result, runErr := runner.Run(ctx, prompt)
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
						messages = append(messages, goai.NewToolMessage(
							tc.ID, tc.Name, "Error: permission denied by user", true,
						))
					default:
						// Treat as allow once
					}
				}

				prompt = ""  // resume with no new prompt
				continue     // re-enter loop with updated messages + rules
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

// loadEnvFile loads environment variables from a file.
func loadEnvFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if os.Getenv(key) == "" {
			os.Setenv(key, value)
		}
	}
}
