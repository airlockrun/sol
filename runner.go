package sol

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/airlockrun/goai"
	"github.com/airlockrun/goai/message"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/agent"
	"github.com/airlockrun/sol/bus"
	"github.com/airlockrun/sol/provider"
	"github.com/airlockrun/sol/session"
	"github.com/airlockrun/sol/tools"
)

// Runner executes an agent session.
type Runner struct {
	mu sync.Mutex

	// Agent configuration
	agent *agent.Agent

	// Parsed from Agent.Model
	providerID string
	modelID    string

	// Runtime options
	apiKey  string
	baseURL string
	workDir string
	quiet   bool

	// Model and tools
	model    stream.Model
	toolSet  tool.Set
	executor tool.Executor // nil → goai uses LocalExecutor
	parent   *Runner       // Parent runner for subagents

	// Bus and managers (scoped per-run for multi-tenant isolation)
	bus           *bus.Bus
	permissionMgr *bus.PermissionManager
	questionMgr   *bus.QuestionManager

	// State
	session          *session.Session            // For message history and compaction
	sessionID        string
	store            session.SessionStore       // nil = no persistence (CLI mode)
	compactionConfig *session.CompactionConfig  // nil = use defaults
	messages        []goai.Message
	initialMessages []goai.Message             // Pre-loaded thread history (ignored when store is set)
	newMessages     []goai.Message             // append-only: messages generated during this run
	compactionState *CompactionState           // set if compaction happened
	retryStatus     session.RetryStatus
	doomDetector    *session.DoomLoopDetector
}

// RunnerOptions configures a new runner.
type RunnerOptions struct {
	// Agent defines the agent to run (model, tools, prompt, etc.).
	Agent *agent.Agent

	// APIKey is the API key for the provider implied by Agent.Model.
	APIKey string

	// BaseURL is an optional override for OpenAI-compatible endpoints.
	BaseURL string

	// WorkDir is the tool execution working directory.
	WorkDir string

	// Executor handles tool execution. When nil, goai falls back to
	// tool.NewLocalExecutor (tools run in-process). Set this to use remote
	// execution via containers.
	Executor tool.Executor

	// Bus is a scoped event bus for this runner. If nil, a new bus is created.
	Bus *bus.Bus

	// InitialMessages, when set, replaces the default [system, user] message
	// initialization in Run(). The prompt parameter becomes a new user message
	// appended at the end. If prompt is empty, no user message is appended.
	// Ignored when SessionStore is set.
	InitialMessages []goai.Message

	// SessionStore provides pluggable message persistence. When set, messages
	// are loaded from the store (instead of InitialMessages) and new messages
	// are persisted after each step. The store must be pre-scoped to a single
	// conversation by the caller.
	SessionStore session.SessionStore

	// CompactionConfig overrides the default compaction configuration.
	// Use this to set a custom PrunedMessage callback.
	CompactionConfig *session.CompactionConfig

	// Quiet suppresses logging output.
	Quiet bool

	// Model overrides the model instance. If nil, created from Agent.Model + APIKey.
	// Used for testing with mock models.
	Model stream.Model
}

// NewRunner creates a new agent runner.
func NewRunner(opts RunnerOptions) *Runner {
	if opts.Agent == nil {
		panic("RunnerOptions.Agent is required")
	}

	// Parse provider/model from Agent.Model
	providerID, modelID := provider.ParseModel(opts.Agent.Model)

	// Create or use provided model
	model := opts.Model
	if model == nil && opts.Agent.Model != "" {
		model = provider.CreateModel(providerID, modelID, provider.Options{
			APIKey:  opts.APIKey,
			BaseURL: opts.BaseURL,
		})
	}

	// Initialize scoped bus
	b := opts.Bus
	if b == nil {
		b = bus.New()
	}

	r := &Runner{
		agent:           opts.Agent,
		providerID:      providerID,
		modelID:         modelID,
		apiKey:          opts.APIKey,
		baseURL:         opts.BaseURL,
		workDir:         opts.WorkDir,
		quiet:           opts.Quiet,
		model:           model,
		toolSet:         opts.Agent.Tools,
		executor:        opts.Executor,
		initialMessages: opts.InitialMessages,
		store:            opts.SessionStore,
		compactionConfig: opts.CompactionConfig,
		sessionID:        generateSessionID(),
		bus:             b,
		permissionMgr:   bus.NewPermissionManager(b),
		questionMgr:     bus.NewQuestionManager(b),
	}

	return r
}

// log prints output unless quiet mode is enabled.
func (r *Runner) log(format string, args ...any) {
	if r.quiet {
		return
	}
	fmt.Printf(format, args...)
}

// logText prints text output unless quiet mode is enabled.
func (r *Runner) logText(text string) {
	if r.quiet {
		return
	}
	fmt.Print(text)
}

// Run executes the agent with the given prompt and returns the result.
func (r *Runner) Run(ctx context.Context, prompt string) (*RunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Create cancellable context
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Create session for message history and compaction
	contextLimit := provider.GetContextLimit(r.providerID, r.modelID)
	outputLimit := provider.GetOutputLimit(r.providerID, r.modelID)
	if outputLimit == 0 {
		outputLimit = session.OutputTokenMax
	}
	r.session = session.NewWithOptions(session.SessionOptions{
		ID:               r.sessionID,
		AgentName:        r.agent.Name,
		ModelID:          r.modelID,
		CompactionConfig: r.compactionConfig,
		Limits: session.ModelLimits{
			Context: contextLimit,
			Output:  outputLimit,
		},
		Bus: r.bus,
	})

	// Build system prompt
	systemPrompt := r.buildSystemPrompt()

	// Initialize messages — always prepend system prompt.
	// Three modes:
	//   1. SessionStore set: load history from store, prompt = new user message
	//   2. InitialMessages set: use as history (legacy), prompt appended if non-empty
	//   3. Neither: simple [system, user] conversation
	if r.store != nil {
		history, err := r.store.Load(ctx)
		if err != nil {
			return nil, fmt.Errorf("store load: %w", err)
		}
		// Strip image/file parts in messages older than FilesRetainTurns
		// and replace them with the configured detach note. This lets
		// attached files stay visible for a few turns, then disappear
		// with an explicit instruction to re-attach via attachToContext.
		history = r.stripOldFilesFromHistory(history)
		// Populate session with loaded history for compaction tracking.
		r.session.Messages = history

		// Convert to goai format, applying history policy.
		goaiHistory := session.MessagesToGoAI(history)
		r.messages = make([]goai.Message, 0, 1+len(goaiHistory)+1)
		r.messages = append(r.messages, goai.NewSystemMessage(systemPrompt))
		policy := r.agent.HistoryPolicy
		for _, m := range goaiHistory {
			if m.Role == "system" {
				continue
			}
			if m.Role == "tool" && policy.ExcludeToolCalls {
				continue
			}
			m = filterMessageParts(m, policy)
			r.messages = append(r.messages, m)
		}
		if prompt != "" {
			userMsg := goai.NewUserMessage(prompt)
			r.messages = append(r.messages, userMsg)
			// Track user message in session and persist to store.
			sessionUserMsg := session.FromGoAIMessage(userMsg)
			r.session.AddMessage(sessionUserMsg)
			if err := r.store.Append(ctx, []session.Message{sessionUserMsg}); err != nil {
				return nil, fmt.Errorf("store append user message: %w", err)
			}
		}
	} else if r.initialMessages != nil {
		// Legacy path: caller provides pre-loaded messages.
		// Any system messages in initialMessages are stripped (they may come from
		// a checkpoint or a caller that included its own system prompt).
		r.messages = make([]goai.Message, 0, 1+len(r.initialMessages)+1)
		r.messages = append(r.messages, goai.NewSystemMessage(systemPrompt))
		policy := r.agent.HistoryPolicy
		for _, m := range r.initialMessages {
			if m.Role == "system" {
				continue
			}
			if m.Role == "tool" && policy.ExcludeToolCalls {
				continue
			}
			m = filterMessageParts(m, policy)
			r.messages = append(r.messages, m)
		}
		if prompt != "" {
			r.messages = append(r.messages, goai.NewUserMessage(prompt))
		}
	} else {
		r.messages = []goai.Message{
			goai.NewSystemMessage(systemPrompt),
			goai.NewUserMessage(prompt),
		}
	}
	r.newMessages = nil
	r.compactionState = nil

	result := &RunResult{
		AgentName: r.agent.Name,
	}
	// Fill result.Usage from completed Steps before returning, regardless of
	// which branch (complete/fail/cancel/suspend) we took. Callers publishing
	// run-level totals read from result.Usage.
	defer func() { result.Usage = sumStepsUsage(result.Steps) }()

	// Determine max steps
	maxSteps := r.agent.MaxSteps
	if maxSteps == 0 {
		maxSteps = 50
	}

	// Initialize retry status and doom loop detector
	r.retryStatus = session.NewRetryStatus()
	r.doomDetector = session.NewDoomLoopDetector()

	// Run the thinking loop
	for step := range maxSteps {
		r.log("[%s] Step %d...\n", r.agent.Name, step+1)

		stepResult, err := r.runStep(ctx)
		if err != nil {
			// Check for suspension (permission/question needed)
			if suspResult, ok := r.handleSuspension(err, stepResult, result); ok {
				return suspResult, nil
			}

			// Check for context cancellation
			if ctx.Err() != nil {
				result.Status = RunCancelled
				result.Messages = r.copyMessages()
				result.NewMessages = r.copyNewMessages()
				result.CompactionState = r.compactionState
				result.Error = ctx.Err()
				return result, ctx.Err()
			}

			// Check if error is retryable
			if reason := session.RetryableError(err); reason != "" && r.retryStatus.Attempt < session.MaxRetryAttempts {
				r.retryStatus.Attempt++
				delay := session.RetryDelay(r.retryStatus.Attempt, nil)
				r.retryStatus.SetRetrying(r.retryStatus.Attempt, reason, time.Now().Add(delay))

				r.log("[%s] Retry %d/%d in %v: %s\n",
					r.agent.Name, r.retryStatus.Attempt, session.MaxRetryAttempts, delay, reason)

				select {
				case <-time.After(delay):
					continue
				case <-ctx.Done():
					result.Status = RunCancelled
					result.Messages = r.copyMessages()
					result.NewMessages = r.copyNewMessages()
					result.CompactionState = r.compactionState
					result.Error = ctx.Err()
					return result, ctx.Err()
				}
			}

			result.Status = RunFailed
			result.Messages = r.copyMessages()
			result.NewMessages = r.copyNewMessages()
			result.CompactionState = r.compactionState
			result.Error = err
			return result, fmt.Errorf("step %d error: %w", step+1, err)
		}

		// Reset retry status on success
		r.retryStatus.SetIdle()

		result.Steps = append(result.Steps, stepResult)
		result.TotalText += stepResult.Text

		// Update session tokens
		r.session.UpdateTokens(stepResult.Usage)

		// Check for context overflow and compact if needed
		if r.session.IsOverflow() {
			r.log("[%s] Context overflow detected, compacting...\n", r.agent.Name)

			pruned := r.session.Prune()
			if pruned > 0 {
				r.log("[%s] Pruned %d old tool outputs\n", r.agent.Name, pruned)
			}

			if r.session.IsOverflow() {
				if _, err := r.compactNow(ctx, systemPrompt); err != nil {
					result.Status = RunFailed
					result.Messages = r.copyMessages()
					result.NewMessages = r.copyNewMessages()
					result.CompactionState = r.compactionState
					result.Error = err
					return result, err
				}
				r.log("[%s] Context compacted, continuing...\n", r.agent.Name)
				continue
			}
		}

		// Check if we should stop
		if stepResult.FinishReason != stream.FinishReasonToolCalls {
			break
		}
	}

	result.Status = RunCompleted
	result.Messages = r.copyMessages()
	result.NewMessages = r.copyNewMessages()
	result.CompactionState = r.compactionState
	return result, nil
}

// CompactResult describes the outcome of a user-triggered compaction.
type CompactResult struct {
	// TokensFreed is the estimated delta between pre- and post-compaction
	// context size. Reported to the UI as the divider label.
	TokensFreed int

	// Summary is the text the model produced. Callers may surface it to the
	// user, log it, or ignore it.
	Summary string
}

// Compact runs LLM-backed summarization without stepping the thinking loop.
// Used when the user explicitly asks for compaction (e.g. /compact) rather
// than letting the context-overflow path fire on its own. Requires a
// SessionStore — in-memory sessions have no one to persist the checkpoint to.
func (r *Runner) Compact(ctx context.Context) (*CompactResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.store == nil {
		return nil, errors.New("Compact requires a SessionStore")
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Build session + system prompt the same way Run does, so compaction
	// sees the same model limits, bus, and history policy.
	contextLimit := provider.GetContextLimit(r.providerID, r.modelID)
	outputLimit := provider.GetOutputLimit(r.providerID, r.modelID)
	if outputLimit == 0 {
		outputLimit = session.OutputTokenMax
	}
	r.session = session.NewWithOptions(session.SessionOptions{
		ID:               r.sessionID,
		AgentName:        r.agent.Name,
		ModelID:          r.modelID,
		CompactionConfig: r.compactionConfig,
		Limits:           session.ModelLimits{Context: contextLimit, Output: outputLimit},
		Bus:              r.bus,
	})

	systemPrompt := r.buildSystemPrompt()

	history, err := r.store.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("store load: %w", err)
	}
	r.session.Messages = history

	goaiHistory := session.MessagesToGoAI(history)
	r.messages = make([]goai.Message, 0, 1+len(goaiHistory))
	r.messages = append(r.messages, goai.NewSystemMessage(systemPrompt))
	policy := r.agent.HistoryPolicy
	for _, m := range goaiHistory {
		if m.Role == "system" {
			continue
		}
		if m.Role == "tool" && policy.ExcludeToolCalls {
			continue
		}
		m = filterMessageParts(m, policy)
		r.messages = append(r.messages, m)
	}

	// No turn has run yet so there's no LLM-reported token count. Estimate
	// from the loaded history so compactNow's preTokens is meaningful.
	r.session.Tokens = session.Tokens{Input: session.EstimateMessagesTokens(history)}

	tokensFreed, err := r.compactNow(ctx, systemPrompt)
	if err != nil {
		return nil, err
	}

	// The summary is the most recent assistant message flagged Summary=true.
	var summary string
	for _, m := range r.session.GetMessages() {
		if m.Summary {
			summary = m.Content
		}
	}

	return &CompactResult{TokensFreed: tokensFreed, Summary: summary}, nil
}

// compactNow runs a single compaction step against the current session +
// r.messages, rebuilds r.messages with the summary, and persists to the
// store. Shared by the overflow path in Run/Continue and the user-triggered
// Compact() method so the two can't drift.
func (r *Runner) compactNow(ctx context.Context, systemPrompt string) (int, error) {
	preTokens := r.session.Tokens.Input + r.session.Tokens.Cache.Read + r.session.Tokens.Output

	compactOpts := &session.CompactOptions{
		ProviderOptions: provider.ProviderOptions(r.providerID, r.modelID, r.sessionID),
		MaxOutputTokens: provider.MaxOutputTokens(r.modelID),
	}
	if err := r.session.CompactAndContinue(ctx, r.model, r.messages, compactOpts); err != nil {
		return 0, fmt.Errorf("compaction error: %w", err)
	}

	compactedMsgs := r.session.ToGoAIMessages()
	r.messages = append([]goai.Message{goai.NewSystemMessage(systemPrompt)}, compactedMsgs...)
	r.compactionState = &CompactionState{Messages: compactedMsgs}

	tokensFreed := 0
	if r.store != nil {
		postCheckpoint := r.session.GetMessages()
		tokensFreed = preTokens - session.EstimateMessagesTokens(postCheckpoint)
		if tokensFreed < 0 {
			tokensFreed = 0
		}
		if err := r.store.Compact(ctx, postCheckpoint, tokensFreed); err != nil {
			r.log("[%s] Warning: store compact failed: %v\n", r.agent.Name, err)
		}
	}
	return tokensFreed, nil
}

// copyMessages returns a snapshot of the current thread messages.
func (r *Runner) copyMessages() []goai.Message {
	msgs := make([]goai.Message, len(r.messages))
	copy(msgs, r.messages)
	return msgs
}

// copyNewMessages returns a snapshot of messages generated during this run.
func (r *Runner) copyNewMessages() []goai.Message {
	if len(r.newMessages) == 0 {
		return nil
	}
	msgs := make([]goai.Message, len(r.newMessages))
	copy(msgs, r.newMessages)
	return msgs
}

// Continue adds a new prompt and continues the thread.
// Must be called after Run has been called at least once.
func (r *Runner) Continue(ctx context.Context, prompt string) (*RunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.session == nil {
		return nil, fmt.Errorf("Continue called before Run")
	}

	// Add user message to existing thread
	r.messages = append(r.messages, goai.NewUserMessage(prompt))

	result := &RunResult{
		AgentName: r.agent.Name,
	}
	defer func() { result.Usage = sumStepsUsage(result.Steps) }()

	maxSteps := r.agent.MaxSteps
	if maxSteps == 0 {
		maxSteps = 50
	}

	systemPrompt := r.buildSystemPrompt()

	for step := range maxSteps {
		r.log("[%s] Step %d...\n", r.agent.Name, step+1)

		stepResult, err := r.runStep(ctx)
		if err != nil {
			if suspResult, ok := r.handleSuspension(err, stepResult, result); ok {
				return suspResult, nil
			}

			if ctx.Err() != nil {
				result.Status = RunCancelled
				result.Messages = r.copyMessages()
				result.NewMessages = r.copyNewMessages()
				result.CompactionState = r.compactionState
				result.Error = ctx.Err()
				return result, ctx.Err()
			}

			if reason := session.RetryableError(err); reason != "" && r.retryStatus.Attempt < session.MaxRetryAttempts {
				r.retryStatus.Attempt++
				delay := session.RetryDelay(r.retryStatus.Attempt, nil)
				r.retryStatus.SetRetrying(r.retryStatus.Attempt, reason, time.Now().Add(delay))

				r.log("[%s] Retry %d/%d in %v: %s\n",
					r.agent.Name, r.retryStatus.Attempt, session.MaxRetryAttempts, delay, reason)

				select {
				case <-time.After(delay):
					continue
				case <-ctx.Done():
					result.Status = RunCancelled
					result.Messages = r.copyMessages()
					result.NewMessages = r.copyNewMessages()
					result.CompactionState = r.compactionState
					result.Error = ctx.Err()
					return result, ctx.Err()
				}
			}

			result.Status = RunFailed
			result.Messages = r.copyMessages()
			result.NewMessages = r.copyNewMessages()
			result.CompactionState = r.compactionState
			result.Error = err
			return result, fmt.Errorf("step %d error: %w", step+1, err)
		}

		r.retryStatus.SetIdle()
		result.Steps = append(result.Steps, stepResult)
		result.TotalText += stepResult.Text

		r.session.UpdateTokens(stepResult.Usage)

		if r.session.IsOverflow() {
			r.log("[%s] Context overflow detected, compacting...\n", r.agent.Name)

			pruned := r.session.Prune()
			if pruned > 0 {
				r.log("[%s] Pruned %d old tool outputs\n", r.agent.Name, pruned)
			}

			if r.session.IsOverflow() {
				if _, err := r.compactNow(ctx, systemPrompt); err != nil {
					result.Status = RunFailed
					result.Messages = r.copyMessages()
					result.NewMessages = r.copyNewMessages()
					result.CompactionState = r.compactionState
					result.Error = err
					return result, err
				}
				r.log("[%s] Context compacted, continuing...\n", r.agent.Name)
				continue
			}
		}

		if stepResult.FinishReason != stream.FinishReasonToolCalls {
			break
		}
	}

	result.Status = RunCompleted
	result.Messages = r.copyMessages()
	result.NewMessages = r.copyNewMessages()
	result.CompactionState = r.compactionState
	return result, nil
}

// runStep executes a single step of the thinking loop.
func (r *Runner) runStep(ctx context.Context) (*StepResult, error) {
	// Inject scoped bus and managers into context for tools
	ctx = bus.WithBus(ctx, r.bus)
	ctx = bus.WithPermissionManager(ctx, r.permissionMgr)
	ctx = bus.WithQuestionManager(ctx, r.questionMgr)

	// Tool order matching opencode — varies by model
	usePatch := strings.Contains(r.modelID, "gpt-") &&
		!strings.Contains(r.modelID, "oss") &&
		!strings.Contains(r.modelID, "gpt-4")

	var activeTools []string
	if usePatch {
		activeTools = []string{
			"question", "bash", "read", "glob", "grep",
			"task", "webfetch", "todowrite", "todoread", "skill", "apply_patch",
		}
	} else {
		activeTools = []string{
			"question", "bash", "read", "glob", "grep", "edit", "write",
			"task", "webfetch", "todowrite", "todoread", "skill",
		}
	}

	// Append any additional tools (MCP, custom) not in the hardcoded list.
	// Sorted for deterministic ordering in replay tests.
	known := make(map[string]struct{}, len(activeTools))
	for _, name := range activeTools {
		known[name] = struct{}{}
	}
	var extraTools []string
	for name := range r.toolSet {
		if _, ok := known[name]; !ok {
			extraTools = append(extraTools, name)
		}
	}
	sort.Strings(extraTools)
	activeTools = append(activeTools, extraTools...)

	// Get provider-specific options
	providerOpts := provider.ProviderOptions(r.providerID, r.modelID, r.sessionID)

	// Get max output tokens
	maxTokens := provider.MaxOutputTokens(r.modelID)

	input := stream.Input{
		Model:           r.model,
		Messages:        r.messages,
		Tools:           r.toolSet,
		ActiveTools:     activeTools,
		MaxOutputTokens: &maxTokens,
		ToolChoice:      "auto",
		ProviderOptions: providerOpts,
		Executor:        r.executor,
	}

	// Apply agent temperature if set
	if r.agent.Temperature != nil {
		input.Temperature = r.agent.Temperature
	}

	// Stream the response
	streamResult, err := goai.StreamText(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("stream error: %w", err)
	}

	// Process stream events
	var textBuilder strings.Builder
	var toolCalls []stream.ToolCall
	var toolResults []stream.ToolResultEvent
	var reasoningParts []goai.ReasoningPart

	for event := range streamResult.FullStream {
		switch e := event.Data.(type) {
		case stream.TextDeltaEvent:
			r.bus.Publish(bus.StreamTextDelta, e)
			r.logText(e.Text)
			textBuilder.WriteString(e.Text)
		case stream.ReasoningEndEvent:
			providerOpts := e.ProviderMetadata
			if providerOpts == nil {
				providerOpts = make(map[string]any)
			}
			providerOpts["itemId"] = e.ID
			rp := goai.ReasoningPart{
				ProviderOptions: providerOpts,
			}
			reasoningParts = append(reasoningParts, rp)
		case stream.ToolCallEvent:
			r.bus.Publish(bus.StreamToolCall, e)
			toolCalls = append(toolCalls, stream.ToolCall{
				ID:    e.ToolCallID,
				Name:  e.ToolName,
				Input: e.Input,
			})
			inputSummary := string(e.Input)
			if len(inputSummary) > 200 {
				inputSummary = inputSummary[:200] + "..."
			}
			r.log("\n[tool] %s %s\n", e.ToolName, inputSummary)

			// Check for doom loop
			if r.doomDetector != nil && r.doomDetector.RecordCall(e.ToolName, e.Input) {
				r.log("[warning] Doom loop detected: %s called %d times with same args\n",
					e.ToolName, session.DoomLoopThreshold)
				if err := session.AskDoomLoopPermission(ctx, r.sessionID, e.ToolName); err != nil {
					return nil, fmt.Errorf("doom loop: %w", err)
				}
				r.doomDetector.Reset()
			}
		case stream.ToolResultEvent:
			r.bus.Publish(bus.StreamToolResult, e)
			toolResults = append(toolResults, e)
			output := e.Output.Output
			if len(output) > 500 {
				output = output[:500] + "..."
			}
			r.log("[result] %s\n", output)
		case stream.ErrorEvent:
			var permErr *bus.ErrPermissionNeeded
			var questErr *bus.ErrQuestionNeeded
			if errors.As(e.Error, &permErr) || errors.As(e.Error, &questErr) {
				// Partial step — stream didn't complete, so no usage is available.
				r.appendPartialStep(ctx, textBuilder.String(), reasoningParts, toolCalls, toolResults, stream.Usage{})

				// Re-publish suspension events on the runner's bus so the
				// bridge can forward them to the frontend in real-time.
				// The original event was published on the toolserver's bus
				// which the bridge doesn't subscribe to.
				if questErr != nil {
					r.bus.Publish(bus.QuestionAsked, bus.QuestionAskedPayload{
						Questions: questErr.Questions,
						Tool:      &bus.ToolContext{CallID: questErr.ToolCallID},
					})
				}
				if permErr != nil {
					r.bus.Publish(bus.PermissionAsked, bus.PermissionAskedPayload{
						Permission: permErr.Permission,
						Patterns:   permErr.Patterns,
						ToolCallID: permErr.ToolCallID,
						Metadata:   permErr.Metadata,
					})
				}

				return &StepResult{
					Text:        textBuilder.String(),
					ToolCalls:   toolCalls,
					ToolResults: toolResults,
				}, fmt.Errorf("stream error: %w", e.Error)
			}
			return nil, fmt.Errorf("stream error: %w", e.Error)
		}
	}

	finishReason := streamResult.FinishReason()
	text := textBuilder.String()
	usage := streamResult.Usage()

	stepResult := &StepResult{
		Text:         text,
		ToolCalls:    toolCalls,
		ToolResults:  toolResults,
		FinishReason: finishReason,
		Usage:        usage,
	}

	r.bus.Publish(bus.StreamStepComplete, stepResult)

	r.appendMessages(ctx, text, reasoningParts, toolCalls, toolResults, usage)

	return stepResult, nil
}

// appendPartialStep appends the assistant message and completed tool results to r.messages
// for a partially completed step (e.g., when a tool needs permission/question). Usage is
// typically zero here because the stream did not complete.
func (r *Runner) appendPartialStep(ctx context.Context, text string, reasoningParts []goai.ReasoningPart, toolCalls []stream.ToolCall, toolResults []stream.ToolResultEvent, usage stream.Usage) {
	r.appendMessages(ctx, text, reasoningParts, toolCalls, toolResults, usage)
}

// appendMessages builds and appends assistant + tool result messages to r.messages and r.newMessages.
// Also updates session history and persists via store if configured. The step's Usage is attached
// to the assistant session message so billing / display totals can be computed per-message.
func (r *Runner) appendMessages(ctx context.Context, text string, reasoningParts []goai.ReasoningPart, toolCalls []stream.ToolCall, toolResults []stream.ToolResultEvent, usage stream.Usage) {
	var goaiMsgs []goai.Message

	hasParts := len(toolCalls) > 0 || len(reasoningParts) > 0
	if hasParts {
		parts := make([]goai.Part, 0)
		for _, rp := range reasoningParts {
			parts = append(parts, rp)
		}
		if text != "" {
			parts = append(parts, goai.TextPart{Text: text})
		}
		for _, tc := range toolCalls {
			parts = append(parts, goai.ToolCallPart{
				ID:    tc.ID,
				Name:  tc.Name,
				Input: tc.Input,
			})
		}
		assistantMsg := goai.NewAssistantMessageWithParts(parts...)
		r.messages = append(r.messages, assistantMsg)
		r.newMessages = append(r.newMessages, assistantMsg)
		goaiMsgs = append(goaiMsgs, assistantMsg)

		for _, tr := range toolResults {
			toolMsg := goai.NewToolMessage(
				tr.ToolCallID,
				tr.ToolName,
				tr.Output.Output,
				false,
			)
			// Convert tool result attachments to message content parts
			// so the LLM can see files on the next turn.
			for _, att := range tr.Output.Attachments {
				if strings.HasPrefix(att.MimeType, "image/") {
					toolMsg.Content.Parts = append(toolMsg.Content.Parts,
						message.ImagePart{Image: att.Data, MimeType: att.MimeType})
				} else {
					toolMsg.Content.Parts = append(toolMsg.Content.Parts,
						message.FilePart{Data: att.Data, MimeType: att.MimeType, Filename: att.Filename})
				}
			}
			r.messages = append(r.messages, toolMsg)
			r.newMessages = append(r.newMessages, toolMsg)
			goaiMsgs = append(goaiMsgs, toolMsg)
		}
	} else if text != "" {
		assistantMsg := goai.NewAssistantMessage(text)
		r.messages = append(r.messages, assistantMsg)
		r.newMessages = append(r.newMessages, assistantMsg)
		goaiMsgs = append(goaiMsgs, assistantMsg)
	}

	// Track in session history (for compaction) and persist via store.
	if len(goaiMsgs) > 0 {
		sessionMsgs := session.FromGoAIMessages(goaiMsgs)

		// Attach this step's LLM usage to the assistant session message so
		// per-message token counts can be persisted (runs/cleared totals rely
		// on them). Tool messages don't carry their own LLM cost.
		for i := range sessionMsgs {
			if sessionMsgs[i].Role == "assistant" {
				sessionMsgs[i].Tokens = session.Tokens{
					Input:  usage.InputTotal(),
					Output: usage.OutputTotal(),
				}
				break
			}
		}

		// Patch Source from tool attachments (goai parts have no Source field).
		for _, tr := range toolResults {
			for _, att := range tr.Output.Attachments {
				if att.Filename == "" {
					continue
				}
				for i := range sessionMsgs {
					for j := range sessionMsgs[i].Parts {
						p := &sessionMsgs[i].Parts[j]
						if p.Type == "image" && p.Image != nil && p.Image.Source == "" && p.Image.MimeType == att.MimeType {
							p.Image.Source = att.Filename
						}
						if p.Type == "file" && p.File != nil && p.File.Source == "" && p.File.MimeType == att.MimeType {
							p.File.Source = att.Filename
						}
					}
				}
			}
		}

		// Strip image/file parts and append detach info to tool output before
		// persisting when ExcludeFiles is set. The detach message goes into the
		// tool Output string (not a separate text part) so it works with ALL
		// providers, including those that don't support multi-part tool results.
		if r.agent.HistoryPolicy.ExcludeFiles {
			prunedMsg := r.session.CompactionConfig.PrunedMessage
			if prunedMsg == nil {
				prunedMsg = session.DefaultPrunedMessage
			}

			for i := range sessionMsgs {
				// Collect detach messages for stripped images/files.
				var detachNotes []string
				var filtered []session.Part
				for _, p := range sessionMsgs[i].Parts {
					switch p.Type {
					case "image":
						if p.Image != nil {
							detachNotes = append(detachNotes, prunedMsg(session.PrunedInfo{
								Type:     "image",
								MimeType: p.Image.MimeType,
								Source:   p.Image.Source,
							}))
							continue // drop image part
						}
					case "file":
						if p.File != nil {
							detachNotes = append(detachNotes, prunedMsg(session.PrunedInfo{
								Type:     "file",
								MimeType: p.File.MimeType,
								Filename: p.File.Filename,
								Source:   p.File.Source,
							}))
							continue // drop file part
						}
					}
					filtered = append(filtered, p)
				}
				// Append detach notes to the tool output string.
				if len(detachNotes) > 0 {
					for j := range filtered {
						if filtered[j].Type == "tool" && filtered[j].Tool != nil {
							for _, note := range detachNotes {
								filtered[j].Tool.Output += "\n" + note
							}
							break // attach to first tool part
						}
					}
				}
				sessionMsgs[i].Parts = filtered
			}
		}

		for _, sm := range sessionMsgs {
			r.session.AddMessage(sm)
		}
		if r.store != nil {
			if err := r.store.Append(ctx, sessionMsgs); err != nil {
				r.log("[%s] Warning: store append failed: %v\n", r.agent.Name, err)
			}
		}
	}
}

// stripOldFilesFromHistory enforces HistoryPolicy.FilesRetainTurns by
// keeping image/file parts only in the N most recent user turns (plus
// their trailing assistant/tool responses). Older messages get their
// image/file parts replaced with a detach note — appended to the nearest
// tool output, or inserted as a text part if there is none.
//
// A "turn" is bounded by a user message: each user msg starts a new turn
// and owns every non-user msg that follows it until the next user msg.
// Messages before the Nth-most-recent user msg (in chronological order)
// fall outside the retention window and get stripped.
//
// When FilesRetainTurns is 0 (default), this is a no-op — the
// conventional ExcludeFiles path handles strip-at-persist for callers
// that want immediate eviction.
func (r *Runner) stripOldFilesFromHistory(history []session.Message) []session.Message {
	retain := r.agent.HistoryPolicy.FilesRetainTurns
	if retain <= 0 {
		return history
	}

	// Locate user-turn boundaries.
	var userIdx []int
	for i, m := range history {
		if m.Role == "user" {
			userIdx = append(userIdx, i)
		}
	}
	if len(userIdx) <= retain {
		return history
	}
	stripBefore := userIdx[len(userIdx)-retain]

	prunedMsg := r.session.CompactionConfig.PrunedMessage
	if prunedMsg == nil {
		prunedMsg = session.DefaultPrunedMessage
	}

	for i := 0; i < stripBefore; i++ {
		var detachNotes []string
		var filtered []session.Part
		for _, p := range history[i].Parts {
			switch p.Type {
			case "image":
				if p.Image != nil {
					detachNotes = append(detachNotes, prunedMsg(session.PrunedInfo{
						Type:     "image",
						MimeType: p.Image.MimeType,
						Source:   p.Image.Source,
					}))
					continue
				}
			case "file":
				if p.File != nil {
					detachNotes = append(detachNotes, prunedMsg(session.PrunedInfo{
						Type:     "file",
						MimeType: p.File.MimeType,
						Filename: p.File.Filename,
						Source:   p.File.Source,
					}))
					continue
				}
			}
			filtered = append(filtered, p)
		}
		if len(detachNotes) > 0 {
			attached := false
			for j := range filtered {
				if filtered[j].Type == "tool" && filtered[j].Tool != nil {
					for _, note := range detachNotes {
						filtered[j].Tool.Output += "\n" + note
					}
					attached = true
					break
				}
			}
			if !attached {
				for _, note := range detachNotes {
					filtered = append(filtered, session.Part{Type: "text", Text: note})
				}
			}
		}
		history[i].Parts = filtered
	}
	return history
}

// filterMessageParts strips message parts based on the history policy.
// Only applied to InitialMessages (history), not messages from the current run.
func filterMessageParts(msg goai.Message, policy agent.HistoryPolicy) goai.Message {
	if !msg.Content.IsMultiPart() {
		return msg
	}

	filtered := make([]message.Part, 0, len(msg.Content.Parts))
	for _, p := range msg.Content.Parts {
		switch p.(type) {
		case goai.ReasoningPart:
			if policy.ExcludeReasoning {
				continue
			}
		case goai.ToolCallPart:
			if policy.ExcludeToolCalls {
				continue
			}
		case message.ImagePart, message.FilePart:
			if policy.ExcludeFiles {
				continue
			}
		}
		filtered = append(filtered, p)
	}

	if len(filtered) == 0 {
		return goai.Message{Role: msg.Role}
	}

	// Simplify to text-only if only one TextPart remains.
	if len(filtered) == 1 {
		if tp, ok := filtered[0].(goai.TextPart); ok {
			return goai.Message{Role: msg.Role, Content: message.Content{Text: tp.Text}}
		}
	}

	return goai.Message{Role: msg.Role, Content: message.Content{Parts: filtered}}
}

// handleSuspension checks if the error is a permission/question suspension
// and populates the RunResult accordingly.
func (r *Runner) handleSuspension(err error, stepResult *StepResult, result *RunResult) (*RunResult, bool) {
	var permErr *bus.ErrPermissionNeeded
	var questErr *bus.ErrQuestionNeeded
	if errors.As(err, &permErr) {
		result.Status = RunSuspended
		result.Messages = r.copyMessages()
		result.NewMessages = r.copyNewMessages()
		result.CompactionState = r.compactionState
		result.SuspensionContext = &SuspensionContext{
			Reason:           "permission",
			Data:             permErr,
			PendingToolCalls: pendingToolCalls(stepResult.ToolCalls, stepResult.ToolResults),
			CompletedResults: stepResult.ToolResults,
		}
		return result, true
	}
	if errors.As(err, &questErr) {
		result.Status = RunSuspended
		result.Messages = r.copyMessages()
		result.NewMessages = r.copyNewMessages()
		result.CompactionState = r.compactionState
		result.SuspensionContext = &SuspensionContext{
			Reason:           "question",
			Data:             questErr,
			PendingToolCalls: pendingToolCalls(stepResult.ToolCalls, stepResult.ToolResults),
			CompletedResults: stepResult.ToolResults,
		}
		return result, true
	}
	return nil, false
}

// pendingToolCalls returns tool calls that have not yet completed.
func pendingToolCalls(allCalls []stream.ToolCall, completed []stream.ToolResultEvent) []stream.ToolCall {
	done := make(map[string]bool)
	for _, r := range completed {
		done[r.ToolCallID] = true
	}
	var pending []stream.ToolCall
	for _, tc := range allCalls {
		if !done[tc.ID] {
			pending = append(pending, tc)
		}
	}
	return pending
}

// buildSystemPrompt builds the system prompt for this agent.
func (r *Runner) buildSystemPrompt() string {
	basePrompt := r.agent.SystemPrompt
	if basePrompt == "" {
		// No custom prompt — use Sol's model-specific default (includes env section).
		basePrompt = SystemPrompt(r.modelID, r.workDir)
	} else if r.agent.EnvironmentPrompt != "" {
		// Agent provides custom environment context (e.g. agentsdk agents).
		basePrompt += "\n\n" + r.agent.EnvironmentPrompt
	}

	// Add subagent context if this is a subagent
	if r.parent != nil {
		basePrompt += "\n\nYou are a subagent spawned to handle a specific task. " +
			"Focus on completing the assigned task efficiently and report your results clearly."
	}

	return basePrompt
}

// Bus returns the runner's scoped event bus.
func (r *Runner) Bus() *bus.Bus {
	return r.bus
}

// PermissionManager returns the runner's scoped permission manager.
func (r *Runner) PermissionManager() *bus.PermissionManager {
	return r.permissionMgr
}

// QuestionManager returns the runner's scoped question manager.
func (r *Runner) QuestionManager() *bus.QuestionManager {
	return r.questionMgr
}

// AgentName returns the name of the current agent.
// This implements tools.SubagentSpawner interface.
func (r *Runner) AgentName() string {
	return r.agent.Name
}

// SpawnSubagent creates and runs a subagent.
// This implements tools.SubagentSpawner interface.
func (r *Runner) SpawnSubagent(ctx context.Context, agentName string, prompt string) (tools.SubagentResult, error) {
	factory, exists := agent.GetFactory(agentName)
	if !exists {
		return nil, fmt.Errorf("agent %q not found", agentName)
	}

	subagent := factory(r.modelID)
	subagent.Model = r.agent.Model // inherit parent's model

	subRunner := NewRunner(RunnerOptions{
		Agent:   subagent,
		APIKey:  r.apiKey,
		BaseURL: r.baseURL,
		WorkDir: r.workDir,
		Bus:     r.bus,
		Quiet:   true,
		Model:   r.model,
	})
	subRunner.parent = r

	return subRunner.Run(ctx, prompt)
}

// generateSessionID creates a unique session ID for prompt caching.
func generateSessionID() string {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("ses_%d", time.Now().UnixNano())
	}
	encoded := base64.URLEncoding.EncodeToString(b)
	encoded = strings.ReplaceAll(encoded, "-", "")
	encoded = strings.ReplaceAll(encoded, "_", "")
	encoded = strings.ReplaceAll(encoded, "=", "")
	return "ses_" + encoded
}

// RunStatus represents the outcome of a run.
type RunStatus string

const (
	RunCompleted RunStatus = "completed"
	RunSuspended RunStatus = "suspended"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

// SuspensionContext captures state when a run is suspended.
type SuspensionContext struct {
	Reason           string                   `json:"reason"`
	Data             any                      `json:"data,omitempty"`
	PendingToolCalls []stream.ToolCall        `json:"pendingToolCalls,omitempty"`
	CompletedResults []stream.ToolResultEvent `json:"completedResults,omitempty"`
}

// CompactionState captures the compacted messages when context overflow triggers compaction.
type CompactionState struct {
	Messages []goai.Message `json:"messages"` // the compacted messages (excluding system prompt)
}

// RunResult contains the results of an agent run.
type RunResult struct {
	AgentName string        `json:"agentName"`
	TotalText string        `json:"totalText"`
	Steps     []*StepResult `json:"-"`
	Error     error         `json:"-"`

	Status            RunStatus          `json:"status"`
	Messages          []goai.Message     `json:"messages"`
	NewMessages       []goai.Message     `json:"newMessages,omitempty"`
	CompactionState   *CompactionState   `json:"compactionState,omitempty"`
	SuspensionContext *SuspensionContext  `json:"suspensionContext,omitempty"`

	// Usage is the sum of every step's LLM usage over the run. InputTotal
	// and OutputTotal reflect total tokens billed for all API calls this
	// run made (tool loops included). Callers publishing billing / display
	// totals should read from here.
	Usage stream.Usage `json:"usage,omitempty"`
}

// GetTotalText returns the total text output.
// This implements tools.SubagentResult interface.
func (r *RunResult) GetTotalText() string {
	return r.TotalText
}

// sumStepsUsage accumulates per-step usage into a single total. Input and
// output tokens both accumulate across steps because each provider call
// bills independently — replacing (not summing) would undercount tool-loop
// runs. Cache/reasoning fields left zero for now; billing cares about
// totals first, breakdown later.
func sumStepsUsage(steps []*StepResult) stream.Usage {
	var in, out int
	for _, s := range steps {
		if s == nil {
			continue
		}
		in += s.Usage.InputTotal()
		out += s.Usage.OutputTotal()
	}
	if in == 0 && out == 0 {
		return stream.Usage{}
	}
	return stream.Usage{
		InputTokens:  stream.InputTokens{Total: stream.IntPtr(in)},
		OutputTokens: stream.OutputTokens{Total: stream.IntPtr(out)},
	}
}

// StepResult contains the results of a single step.
type StepResult struct {
	Text         string
	ToolCalls    []stream.ToolCall
	ToolResults  []stream.ToolResultEvent
	FinishReason stream.FinishReason
	Usage        stream.Usage
}
