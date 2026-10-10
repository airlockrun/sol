package main

import (
	"fmt"
	"reflect"
	"testing"
)

func TestParseCLI(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want cliOptions
	}{
		{"session words", []string{"session", "list"}, cliOptions{prompt: "session list"}},
		{"auth words", []string{"auth", "login", "work/openai", "--method", "codex"}, cliOptions{prompt: "auth login work/openai --method codex"}},
		{"config words", []string{"config", "agent", "plan"}, cliOptions{prompt: "config agent plan"}},
		{"clear word", []string{"clear"}, cliOptions{prompt: "clear"}},
		{"compact word", []string{"compact"}, cliOptions{prompt: "compact"}},
		{"prompt options literal", []string{"request", "--session", "list", "--unknown", "--"}, cliOptions{prompt: "request --session list --unknown --"}},
		{"end options help", []string{"--", "--help"}, cliOptions{prompt: "--help"}},
		{"end options clear", []string{"--", "--compact"}, cliOptions{prompt: "--compact"}},
		{"end options entire prompt", []string{"--", "--", "--auth", "status"}, cliOptions{prompt: "-- --auth status"}},
		{"selected prompt", []string{"--sid", "selected", "session", "list"}, cliOptions{id: "selected", prompt: "session list"}},
		{"model ref", []string{"--model=work/huggingface/org/model", "--agent", "plan", "--verbose", "hello"}, cliOptions{model: "work/huggingface/org/model", agent: "plan", verbose: true, prompt: "hello"}},
		{"operational single hyphen", []string{"-model", "work/openai/gpt-5.4", "-agent", "plan", "hello"}, cliOptions{model: "work/openai/gpt-5.4", agent: "plan", prompt: "hello"}},
		{"session action", []string{"--session", "show", "id"}, cliOptions{action: "session", args: []string{"show", "id"}}},
		{"selected compact group", []string{"--sid", "id", "--session", "compact"}, cliOptions{action: "session", id: "id", args: []string{"compact"}}},
		{"auth nested flags", []string{"--auth", "login", "work/openai", "--method", "codex"}, cliOptions{action: "auth", args: []string{"login", "work/openai", "--method", "codex"}}},
		{"config nested clear", []string{"--config", "agent", "--clear"}, cliOptions{action: "config", args: []string{"agent", "--clear"}}},
		{"clear selector first", []string{"--sid", "id", "--clear"}, cliOptions{action: "clear", id: "id"}},
		{"clear selector last", []string{"--clear", "--sid", "id"}, cliOptions{action: "clear", id: "id"}},
		{"compact", []string{"--compact"}, cliOptions{action: "compact"}},
		{"help", []string{"--help"}, cliOptions{action: "help"}},
		{"version", []string{"--version"}, cliOptions{action: "version"}},
		{"action help", []string{"--session", "--help"}, cliOptions{action: "help", args: []string{"--help"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCLI(tc.args)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseCLI = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestParseCLIRejectsInvalidControls(t *testing.T) {
	for _, args := range [][]string{
		nil, {"--"}, {"--unknown"}, {"-session", "id"}, {"-auth", "status"},
		{"-config", "agent"}, {"-compact"}, {"-clear"}, {"-help"}, {"-version"},
		{"--sid"}, {"--sid", ""}, {"--sid="}, {"--sid", "--clear"},
		{"--sid", "a", "--sid", "b", "request"}, {"--clear", "--compact"},
		{"--compact", "--clear"}, {"--clear", "request"}, {"--compact", "request"},
		{"--clear", "--model", "work/openai/gpt-5.4"}, {"--sid", "id", "--auth", "status"},
		{"--agent", "plan", "--config", "agent"}, {"--compact", "--auth", "status"},
		{"--help", "request"}, {"--version", "request"}, {"--session=new"},
	} {
		t.Run(fmt.Sprintf("%q", args), func(t *testing.T) {
			if _, err := parseCLI(args); err == nil {
				t.Fatalf("accepted invalid controls: %q", args)
			}
		})
	}
}
