package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

const cliUsage = `Usage:
  sol [options] 'request'
  sol --session new|list|show ID|compact [ID]
  sol --auth login ENTRY [--method codex]
  sol --auth set-key ENTRY [--stdin]
  sol --auth status [ENTRY]
  sol --auth logout|remove-key ENTRY
  sol --config agent [build|plan|explore|general|--clear]
  sol --config model [slug/provider/model|--clear]
  sol [--sid ID] --compact
  sol [--sid ID] --clear
  sol --help
  sol --version

Options (before the first prompt word):
  --sid ID                  Existing session; overrides SOL_SESSION for this invocation
  --model slug/provider/model  Model override
  --agent NAME              Agent override (session/configured default otherwise)
  --name NAME               Agent name for prompts
  --mcp name=url,name2=url2  MCP servers
  --search                  Enable web search
  --search-provider ENTRY   Independent named search API-key account
  --verbose                 Tool progress on stderr

Positional words are always the request, including session, auth and config.
All arguments after the first prompt word are literal. A leading -- ends options.
Batch requests print assistant text to stdout. With no session selector, each
request creates a new session. No global current session is stored.
Compact summarizes model context; clear resets context and retains full history.
Clear requires no model or credentials and changes no workspace files.

Example:
  export SOL_SESSION="$(sol --session new)"
  sol 'Inspect this project'
  sol 'Continue with the change'
  sol --compact
  sol --clear
  sol -- --help
`

type cliOptions struct {
	action, prompt, id, model, agent, name, mcp, searchEntry string
	search, verbose                                          bool
	args                                                     []string
}

// parseCLI stops interpreting options as soon as the prompt starts. Administrative
// groups own their remaining arguments so their nested options cannot become requests.
func parseCLI(args []string) (cliOptions, error) {
	var o cliOptions
	fs := flag.NewFlagSet("sol", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.id, "sid", "", "")
	fs.StringVar(&o.model, "model", "", "")
	fs.StringVar(&o.agent, "agent", "", "")
	fs.StringVar(&o.name, "name", "", "")
	fs.StringVar(&o.mcp, "mcp", "", "")
	fs.StringVar(&o.searchEntry, "search-provider", "", "")
	fs.BoolVar(&o.search, "search", false, "")
	fs.BoolVar(&o.verbose, "verbose", false, "")
	seen := make(map[string]bool)
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		if arg == "--" {
			o.prompt = strings.Join(args, " ")
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			o.prompt = strings.Join(append([]string{arg}, args...), " ")
			break
		}
		key, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		switch key {
		case "auth", "config", "session", "help", "version", "compact", "clear":
			if !strings.HasPrefix(arg, "--") || hasValue || o.action != "" {
				return o, fmt.Errorf("invalid administrative option %q", arg)
			}
			o.action = key
			if key == "auth" || key == "config" || key == "session" {
				for option := range seen {
					if key != "session" || option != "sid" {
						return o, fmt.Errorf("--%s cannot be combined with --%s", key, option)
					}
				}
				o.args = args
				if len(args) > 0 && args[len(args)-1] == "--help" {
					o.action = "help"
				}
				return o, nil
			}
			if key == "help" || key == "version" {
				if len(args) != 0 || len(seen) != 0 {
					return o, fmt.Errorf("--%s must be used alone", key)
				}
				return o, nil
			}
			continue
		}
		// Operational options accept Go's single-hyphen spelling; administrative
		// options and the session selector require the documented double hyphen.
		if !strings.HasPrefix(arg, "--") {
			key, value, hasValue = strings.Cut(strings.TrimPrefix(arg, "-"), "=")
			if key == "sid" {
				return o, errors.New("use --sid ID to select a session")
			}
		}
		f := fs.Lookup(key)
		if f == nil {
			return o, fmt.Errorf("unknown option %q; use sol --help", arg)
		}
		if seen[key] {
			return o, fmt.Errorf("duplicate option --%s", key)
		}
		seen[key] = true
		isBool, ok := f.Value.(interface{ IsBoolFlag() bool })
		if !hasValue {
			if ok && isBool.IsBoolFlag() {
				value = "true"
			} else {
				if len(args) == 0 || strings.HasPrefix(args[0], "--") {
					return o, fmt.Errorf("--%s requires a value", key)
				}
				value, args = args[0], args[1:]
			}
		}
		if err := fs.Set(key, value); err != nil {
			return o, err
		}
		if key == "sid" && value == "" {
			return o, errors.New("--sid requires a nonempty existing session ID")
		}
	}
	if o.action != "" && o.prompt != "" {
		return o, fmt.Errorf("--%s does not accept a request", o.action)
	}
	if o.action == "clear" {
		for option := range seen {
			if option != "sid" {
				return o, fmt.Errorf("--clear cannot be combined with --%s", option)
			}
		}
	}
	if o.action == "" && o.prompt == "" {
		return o, errors.New("a request is required; use sol --help")
	}
	return o, nil
}
