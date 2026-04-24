package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/toolutil"
)

const (
	defaultBashTimeout = 2 * 60 * 1000 // 2 minutes in ms
)

// patchParamDescription replaces the description of a property in a JSON Schema.
func patchParamDescription(schema json.RawMessage, param, desc string) json.RawMessage {
	var s map[string]json.RawMessage
	if json.Unmarshal(schema, &s) != nil {
		return schema
	}
	propsRaw, ok := s["properties"]
	if !ok {
		return schema
	}
	var props map[string]json.RawMessage
	if json.Unmarshal(propsRaw, &props) != nil {
		return schema
	}
	paramRaw, ok := props[param]
	if !ok {
		return schema
	}
	var p map[string]json.RawMessage
	if json.Unmarshal(paramRaw, &p) != nil {
		return schema
	}
	descJSON, _ := json.Marshal(desc)
	p["description"] = descJSON
	props[param], _ = json.Marshal(p)
	s["properties"], _ = json.Marshal(props)
	out, _ := json.Marshal(s)
	return out
}

// extractCommandPattern extracts a simplified command pattern for "always allow"
// e.g., "git commit -m 'message'" -> "git "
// e.g., "npm install package" -> "npm "
// e.g., "rm -rf /tmp/foo" -> "rm "
func extractCommandPattern(command string) string {
	// Split on common separators
	parts := strings.FieldsFunc(command, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '&' || r == '|' || r == ';'
	})

	if len(parts) == 0 {
		return ""
	}

	// Return first word (the command) with a space
	// This allows "always allow" to match any args with that command
	return parts[0] + " "
}

// Commands that commonly access file paths
var pathAccessingCommands = map[string]bool{
	"cd": true, "rm": true, "cp": true, "mv": true,
	"mkdir": true, "touch": true, "chmod": true, "chown": true,
	"cat": true, "ls": true, "ln": true, "rmdir": true,
}

// extractExternalDirectories extracts paths from a command that are outside the working directory
func extractExternalDirectories(command, workDir string) []string {
	var external []string

	// Simple command parsing - split by && ; | and then by spaces
	// This is simpler than tree-sitter but covers common cases
	cmdParts := strings.FieldsFunc(command, func(r rune) bool {
		return r == '&' || r == '|' || r == ';'
	})

	for _, cmdPart := range cmdParts {
		args := parseCommandArgs(strings.TrimSpace(cmdPart))
		if len(args) == 0 {
			continue
		}

		cmdName := filepath.Base(args[0]) // Handle /usr/bin/rm -> rm
		if !pathAccessingCommands[cmdName] {
			continue
		}

		// Check each argument that looks like a path
		for _, arg := range args[1:] {
			// Skip flags
			if strings.HasPrefix(arg, "-") {
				continue
			}
			// Skip chmod mode arguments like +x, 755
			if cmdName == "chmod" && (strings.HasPrefix(arg, "+") || isNumeric(arg)) {
				continue
			}

			// Resolve the path
			resolved := resolvePath(arg, workDir)
			if resolved == "" {
				continue
			}

			// Check if outside working directory
			if !isInsideDir(resolved, workDir) {
				external = append(external, resolved)
			}
		}
	}

	return unique(external)
}

// parseCommandArgs parses a command string into arguments, handling basic quoting
func parseCommandArgs(cmd string) []string {
	var args []string
	var current strings.Builder
	var inSingleQuote, inDoubleQuote bool

	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case c == '\'' && !inDoubleQuote:
			inSingleQuote = !inSingleQuote
		case c == '"' && !inSingleQuote:
			inDoubleQuote = !inDoubleQuote
		case c == ' ' && !inSingleQuote && !inDoubleQuote:
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(c)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

// resolvePath resolves a path relative to workDir, handling ~ and symlinks
func resolvePath(path, workDir string) string {
	// Handle home directory
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		path = filepath.Join(home, path[2:])
	} else if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		path = home
	}

	// Make absolute
	if !filepath.IsAbs(path) {
		path = filepath.Join(workDir, path)
	}

	// Clean the path
	path = filepath.Clean(path)

	// Try to resolve symlinks (but don't fail if path doesn't exist yet)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}

	return path
}

// isInsideDir checks if path is inside or equal to dir
func isInsideDir(path, dir string) bool {
	// Clean and normalize both paths
	path = filepath.Clean(path)
	dir = filepath.Clean(dir)

	// Check if path starts with dir
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}

	// If relative path starts with "..", it's outside
	return !strings.HasPrefix(rel, "..")
}

// isNumeric checks if a string is all digits
func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}

// unique returns unique strings from a slice
func unique(strs []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, s := range strs {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}

// BashInput is the input schema for the bash tool
type BashInput struct {
	Command     string `json:"command" description:"The command to execute"`
	Timeout     int    `json:"timeout,omitempty" description:"Optional timeout in milliseconds"`
	Workdir     string `json:"workdir,omitempty"`
	Description string `json:"description" description:"Clear, concise description of what this command does in 5-10 words. Examples:\nInput: ls\nOutput: Lists files in current directory\n\nInput: git status\nOutput: Shows working tree status\n\nInput: npm install\nOutput: Installs package dependencies\n\nInput: mkdir foo\nOutput: Creates directory 'foo'"`
}

// Bash creates the bash tool. workDir is the default working directory
// shown in the tool description (e.g. "/home/agent/space" or os.Getwd()).
func Bash(workDir string) tool.Tool {
	t := tool.New("bash").
		Description(`Executes a given bash command in a persistent shell session with optional timeout, ensuring proper handling and security measures.

All commands run in ` + workDir + ` by default. Use the ` + "`workdir`" + ` parameter if you need to run a command in a different directory. AVOID using ` + "`cd <directory> && <command>`" + ` patterns - use ` + "`workdir`" + ` instead.

IMPORTANT: This tool is for terminal operations like git, npm, docker, etc. DO NOT use it for file operations (reading, writing, editing, searching, finding files) - use the specialized tools for this instead.

Before executing the command, please follow these steps:

1. Directory Verification:
   - If the command will create new directories or files, first use ` + "`ls`" + ` to verify the parent directory exists and is the correct location
   - For example, before running "mkdir foo/bar", first use ` + "`ls foo`" + ` to check that "foo" exists and is the intended parent directory

2. Command Execution:
   - Always quote file paths that contain spaces with double quotes (e.g., rm "path with spaces/file.txt")
   - Examples of proper quoting:
     - mkdir "/Users/name/My Documents" (correct)
     - mkdir /Users/name/My Documents (incorrect - will fail)
     - python "/path/with spaces/script.py" (correct)
     - python /path/with spaces/script.py (incorrect - will fail)
   - After ensuring proper quoting, execute the command.
   - Capture the output of the command.

Usage notes:
  - The command argument is required.
  - You can specify an optional timeout in milliseconds. If not specified, commands will time out after 120000ms (2 minutes).
  - It is very helpful if you write a clear, concise description of what this command does in 5-10 words.
  - If the output exceeds 2000 lines or 51200 bytes, it will be truncated and the full output will be written to a file. You can use Read with offset/limit to read specific sections or Grep to search the full content. Because of this, you do NOT need to use ` + "`head`" + `, ` + "`tail`" + `, or other truncation commands to limit output - just run the command directly.

  - Avoid using Bash with the ` + "`find`" + `, ` + "`grep`" + `, ` + "`cat`" + `, ` + "`head`" + `, ` + "`tail`" + `, ` + "`sed`" + `, ` + "`awk`" + `, or ` + "`echo`" + ` commands, unless explicitly instructed or when these commands are truly necessary for the task. Instead, always prefer using the dedicated tools for these commands:
    - File search: Use Glob (NOT find or ls)
    - Content search: Use Grep (NOT grep or rg)
    - Read files: Use Read (NOT cat/head/tail)
    - Edit files: Use Edit (NOT sed/awk)
    - Write files: Use Write (NOT echo >/cat <<EOF)
    - Communication: Output text directly (NOT echo/printf)
  - When issuing multiple commands:
    - If the commands are independent and can run in parallel, make multiple Bash tool calls in a single message. For example, if you need to run "git status" and "git diff", send a single message with two Bash tool calls in parallel.
    - If the commands depend on each other and must run sequentially, use a single Bash call with '&&' to chain them together (e.g., ` + "`git add . && git commit -m \"message\" && git push`" + `). For instance, if one operation must complete before another starts (like mkdir before cp, Write before Bash for git operations, or git add before git commit), run these operations sequentially instead.
    - Use ';' only when you need to run commands sequentially but don't care if earlier commands fail
    - DO NOT use newlines to separate commands (newlines are ok in quoted strings)
  - AVOID using ` + "`cd <directory> && <command>`" + `. Use the ` + "`workdir`" + ` parameter to change directories instead.
    <good-example>
    Use workdir="/foo/bar" with command: pytest tests
    </good-example>
    <bad-example>
    cd /foo/bar && pytest tests
    </bad-example>

# Committing changes with git

Only create commits when requested by the user. If unclear, ask first. When the user asks you to create a new git commit, follow these steps carefully:

Git Safety Protocol:
- NEVER update the git config
- NEVER run destructive/irreversible git commands (like push --force, hard reset, etc) unless the user explicitly requests them
- NEVER skip hooks (--no-verify, --no-gpg-sign, etc) unless the user explicitly requests it
- NEVER run force push to main/master, warn the user if they request it
- Avoid git commit --amend. ONLY use --amend when ALL conditions are met:
  (1) User explicitly requested amend, OR commit SUCCEEDED but pre-commit hook auto-modified files that need including
  (2) HEAD commit was created by you in this conversation (verify: git log -1 --format='%an %ae')
  (3) Commit has NOT been pushed to remote (verify: git status shows "Your branch is ahead")
- CRITICAL: If commit FAILED or was REJECTED by hook, NEVER amend - fix the issue and create a NEW commit
- CRITICAL: If you already pushed to remote, NEVER amend unless user explicitly requests it (requires force push)
- NEVER commit changes unless the user explicitly asks you to. It is VERY IMPORTANT to only commit when explicitly asked, otherwise the user will feel that you are being too proactive.

1. You can call multiple tools in a single response. When multiple independent pieces of information are requested and all commands are likely to succeed, run multiple tool calls in parallel for optimal performance. run the following bash commands in parallel, each using the Bash tool:
  - Run a git status command to see all untracked files.
  - Run a git diff command to see both staged and unstaged changes that will be committed.
  - Run a git log command to see recent commit messages, so that you can follow this repository's commit message style.
2. Analyze all staged changes (both previously staged and newly added) and draft a commit message:
  - Summarize the nature of the changes (eg. new feature, enhancement to an existing feature, bug fix, refactoring, test, docs, etc.). Ensure the message accurately reflects the changes and their purpose (i.e. "add" means a wholly new feature, "update" means an enhancement to an existing feature, "fix" means a bug fix, etc.).
  - Do not commit files that likely contain secrets (.env, credentials.json, etc.). Warn the user if they specifically request to commit those files
  - Draft a concise (1-2 sentences) commit message that focuses on the "why" rather than the "what"
  - Ensure it accurately reflects the changes and their purpose
3. You can call multiple tools in a single response. When multiple independent pieces of information are requested and all commands are likely to succeed, run multiple tool calls in parallel for optimal performance. run the following commands:
   - Add relevant untracked files to the staging area.
   - Create the commit with a message
   - Run git status after the commit completes to verify success.
   Note: git status depends on the commit completing, so run it sequentially after the commit.
4. If the commit fails due to pre-commit hook, fix the issue and create a NEW commit (see amend rules above)

Important notes:
- NEVER run additional commands to read or explore code, besides git bash commands
- NEVER use the TodoWrite or Task tools
- DO NOT push to the remote repository unless the user explicitly asks you to do so
- IMPORTANT: Never use git commands with the -i flag (like git rebase -i or git add -i) since they require interactive input which is not supported.
- If there are no changes to commit (i.e., no untracked files and no modifications), do not create an empty commit

# Creating pull requests
Use the gh command via the Bash tool for ALL GitHub-related tasks including working with issues, pull requests, checks, and releases. If given a Github URL use the gh command to get the information needed.

IMPORTANT: When the user asks you to create a pull request, follow these steps carefully:

1. You can call multiple tools in a single response. When multiple independent pieces of information are requested and all commands are likely to succeed, run multiple tool calls in parallel for optimal performance. run the following bash commands in parallel using the Bash tool, in order to understand the current state of the branch since it diverged from the main branch:
   - Run a git status command to see all untracked files
   - Run a git diff command to see both staged and unstaged changes that will be committed
   - Check if the current branch tracks a remote branch and is up to date with the remote, so you know if you need to push to the remote
   - Run a git log command and ` + "`git diff [base-branch]...HEAD`" + ` to understand the full commit history for the current branch (from the time it diverged from the base branch)
2. Analyze all changes that will be included in the pull request, making sure to look at all relevant commits (NOT just the latest commit, but ALL commits that will be included in the pull request!!!), and draft a pull request summary
3. You can call multiple tools in a single response. When multiple independent pieces of information are requested and all commands are likely to succeed, run multiple tool calls in parallel for optimal performance. run the following commands in parallel:
   - Create new branch if needed
   - Push to remote with -u flag if needed
   - Create PR using gh pr create with the format below. Use a HEREDOC to pass the body to ensure correct formatting.
<example>
gh pr create --title "the pr title" --body "$(cat <<'EOF'
## Summary
<1-3 bullet points>
</example>

Important:
- DO NOT use the TodoWrite or Task tools
- Return the PR URL when you're done, so the user can see it

# Other common operations
- View comments on a Github PR: gh api repos/foo/bar/pulls/123/comments
`).
		SchemaFromStruct(BashInput{}).
		Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			var args BashInput
			if err := json.Unmarshal(input, &args); err != nil {
				return tool.Result{}, err
			}

			// Get session ID and working directory for permission
			sessionID, _ := ctx.Value(SessionIDKey).(string)
			workDir, _ := ctx.Value(WorkDirKey).(string)
			if workDir == "" {
				workDir, _ = os.Getwd()
			}

			// Use workdir from args if specified
			effectiveWorkDir := workDir
			if args.Workdir != "" {
				effectiveWorkDir = args.Workdir
			}

			// Check for external directories being accessed
			externalDirs := extractExternalDirectories(args.Command, effectiveWorkDir)
			if len(externalDirs) > 0 {
				// Build "always allow" patterns (parent directories)
				alwaysPatterns := make([]string, len(externalDirs))
				for i, dir := range externalDirs {
					alwaysPatterns[i] = filepath.Dir(dir) + "/*"
				}

				err := AskPermission(ctx, PermissionRequest{
					SessionID:  sessionID,
					Permission: "external_directory",
					Patterns:   externalDirs,
					Always:     alwaysPatterns,
					Metadata: map[string]any{
						"command":     args.Command,
						"directories": externalDirs,
					},
				})
				if err != nil {
					return tool.Result{
						Output: fmt.Sprintf("Error: access to external directory denied: %v", err),
						Title:  fmt.Sprintf("bash: %s (rejected)", args.Description),
					}, nil
				}
			}

			// Request permission to execute bash command
			// Extract a simplified command pattern for the "always allow" option
			cmdPattern := extractCommandPattern(args.Command)
			err := AskPermission(ctx, PermissionRequest{
				SessionID:  sessionID,
				Permission: "bash",
				Patterns:   []string{args.Command},
				Always:     []string{cmdPattern + "*"},
				Metadata: map[string]any{
					"command":     args.Command,
					"description": args.Description,
				},
			})
			if err != nil {
				return tool.Result{
					Output: fmt.Sprintf("Error: %v", err),
					Title:  fmt.Sprintf("bash: %s (rejected)", args.Description),
				}, nil
			}

			// Validate timeout
			if args.Timeout < 0 {
				return tool.Result{
					Output: fmt.Sprintf("Invalid timeout value: %d. Timeout must be a positive number.", args.Timeout),
					Title:  args.Description,
				}, nil
			}

			timeout := args.Timeout
			if timeout == 0 {
				timeout = defaultBashTimeout
			}

			// Create context with timeout
			execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
			defer cancel()

			cmd := exec.CommandContext(execCtx, "bash", "-c", args.Command)
			cmd.Dir = effectiveWorkDir

			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			runErr := cmd.Run()

			// Combine stdout and stderr
			output := stdout.String() + stderr.String()

			// Check for timeout
			timedOut := execCtx.Err() == context.DeadlineExceeded
			if timedOut {
				output += fmt.Sprintf("\n\n<bash_metadata>\nbash tool terminated command after exceeding timeout %d ms\n</bash_metadata>", timeout)
			}

			// Check for other errors
			if runErr != nil && !timedOut {
				output += fmt.Sprintf("\nExit code: %v", runErr)
			}

			// Apply truncation (matching opencode's behavior)
			truncResult := toolutil.TruncateOutput(output, toolutil.TruncateOptions{
				MaxLines:  toolutil.TruncateMaxLines,
				MaxBytes:  toolutil.TruncateMaxBytes,
				Direction: "head",
			})

			title := args.Description
			if title == "" {
				title = truncateStr(args.Command, 50)
			}

			return tool.Result{
				Output: truncResult.Content,
				Title:  title,
			}, nil
		}).
		Build()

	// Patch the workdir param description to include the actual default path
	workdirDesc := "The working directory to run the command in. Defaults to " + workDir + ". Use this instead of 'cd' commands."
	t.InputSchema = patchParamDescription(t.InputSchema, "workdir", workdirDesc)
	return t
}
