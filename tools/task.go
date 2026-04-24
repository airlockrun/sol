package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/airlockrun/goai/tool"
)

// TaskInput is the input schema for the task tool
type TaskInput struct {
	Description  string `json:"description" description:"A short (3-5 words) description of the task"`
	Prompt       string `json:"prompt" description:"The task for the agent to perform"`
	SubagentType string `json:"subagent_type" description:"The type of specialized agent to use for this task"`
	SessionID    string `json:"session_id,omitempty" description:"Existing Task session to continue"`
	Command      string `json:"command,omitempty" description:"The command that triggered this task"`
}

// SubagentResult is the interface for subagent execution results.
type SubagentResult interface {
	GetTotalText() string
}

// SubagentSpawner is the interface for spawning subagents.
// This is implemented by the Runner in the main package.
type SubagentSpawner interface {
	SpawnSubagent(ctx context.Context, agentType, prompt string) (SubagentResult, error)
	AgentName() string
}

// Task creates the task tool
func Task() tool.Tool {
	return tool.New("task").
		Description(`Launch a new agent to handle complex, multistep tasks autonomously.

Available agent types and the tools they have access to:
- general: General-purpose agent for researching complex questions and executing multi-step tasks. Use this agent to execute multiple units of work in parallel.
- explore: Fast agent specialized for exploring codebases. Use this when you need to quickly find files by patterns (eg. "src/components/**/*.tsx"), search code for keywords (eg. "API endpoints"), or answer questions about the codebase (eg. "how do API endpoints work?"). When calling this agent, specify the desired thoroughness level: "quick" for basic searches, "medium" for moderate exploration, or "very thorough" for comprehensive analysis across multiple locations and naming conventions.

When using the Task tool, you must specify a subagent_type parameter to select which agent type to use.

When to use the Task tool:
- When you are instructed to execute custom slash commands. Use the Task tool with the slash command invocation as the entire prompt. The slash command can take arguments. For example: Task(description="Check the file", prompt="/check-file path/to/file.py")

When NOT to use the Task tool:
- If you want to read a specific file path, use the Read or Glob tool instead of the Task tool, to find the match more quickly
- If you are searching for a specific class definition like "class Foo", use the Glob tool instead, to find the match more quickly
- If you are searching for code within a specific file or set of 2-3 files, use the Read tool instead of the Task tool, to find the match more quickly
- Other tasks that are not related to the agent descriptions above


Usage notes:
1. Launch multiple agents concurrently whenever possible, to maximize performance; to do that, use a single message with multiple tool uses
2. When the agent is done, it will return a single message back to you. The result returned by the agent is not visible to the user. To show the user the result, you should send a text message back to the user with a concise summary of the result.
3. Each agent invocation is stateless unless you provide a session_id. Your prompt should contain a highly detailed task description for the agent to perform autonomously and you should specify exactly what information the agent should return back to you in its final and only message to you.
4. The agent's outputs should generally be trusted
5. Clearly tell the agent whether you expect it to write code or just to do research (search, file reads, web fetches, etc.), since it is not aware of the user's intent
6. If the agent description mentions that it should be used proactively, then you should try your best to use it without the user having to ask for it first. Use your judgement.

Example usage (NOTE: The agents below are fictional examples for illustration only - use the actual agents listed above):

<example_agent_descriptions>
"code-reviewer": use this agent after you are done writing a significant piece of code
"greeting-responder": use this agent when to respond to user greetings with a friendly joke
</example_agent_description>

<example>
user: "Please write a function that checks if a number is prime"
assistant: Sure let me write a function that checks if a number is prime
assistant: First let me use the Write tool to write a function that checks if a number is prime
assistant: I'm going to use the Write tool to write the following code:
<code>
function isPrime(n) {
  if (n <= 1) return false
  for (let i = 2; i * i <= n; i++) {
    if (n % i === 0) return false
  }
  return true
}
</code>
<commentary>
Since a significant piece of code was written and the task was completed, now use the code-reviewer agent to review the code
</commentary>
assistant: Now let me use the code-reviewer agent to review the code
assistant: Uses the Task tool to launch the code-reviewer agent
</example>

<example>
user: "Hello"
<commentary>
Since the user is greeting, use the greeting-responder agent to respond with a friendly joke
</commentary>
assistant: "I'm going to use the Task tool to launch the with the greeting-responder agent"
</example>
`).
		SchemaFromStruct(TaskInput{}).
		Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			var args TaskInput
			if err := json.Unmarshal(input, &args); err != nil {
				return tool.Result{}, err
			}

			spawner, ok := ctx.Value(RunnerKey).(SubagentSpawner)
			if !ok || spawner == nil {
				return tool.Result{
					Output: fmt.Sprintf("[Task '%s' - subagent spawning not available in this context]", args.Description),
					Title:  fmt.Sprintf("task: %s", args.Description),
				}, nil
			}

			agentType := args.SubagentType
			if agentType == "" {
				agentType = "explore"
			}

			fmt.Printf("[%s] Spawning subagent '%s' for: %s\n", spawner.AgentName(), agentType, args.Description)

			result, err := spawner.SpawnSubagent(ctx, agentType, args.Prompt)
			if err != nil {
				return tool.Result{
					Output: fmt.Sprintf("Subagent error: %v", err),
					Title:  fmt.Sprintf("task: %s (error)", args.Description),
				}, nil
			}

			output := result.GetTotalText()
			if len(output) > 10000 {
				output = output[:10000] + "\n... [output truncated]"
			}

			return tool.Result{
				Output: output,
				Title:  fmt.Sprintf("task: %s (%s)", args.Description, agentType),
			}, nil
		}).
		Build()
}
