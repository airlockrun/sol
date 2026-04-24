package websearch

import (
	"strings"
	"testing"
)

func TestFormatResponse_RawResults(t *testing.T) {
	resp := &Response{
		Provider: "brave",
		Results: []Result{
			{Title: "Go Programming", URL: "https://go.dev", Snippet: "The Go programming language"},
			{Title: "Go Wiki", URL: "https://go.dev/wiki", Snippet: "Community wiki"},
		},
	}

	out := formatResponse("golang", resp)

	if !strings.Contains(out, `"golang"`) {
		t.Error("output should contain the query")
	}
	if !strings.Contains(out, "brave") {
		t.Error("output should contain the provider name")
	}
	if !strings.Contains(out, "**Go Programming**") {
		t.Error("output should contain bolded title")
	}
	if !strings.Contains(out, "https://go.dev") {
		t.Error("output should contain URL")
	}
	if !strings.Contains(out, "The Go programming language") {
		t.Error("output should contain snippet")
	}
	if strings.Contains(out, "Summary") {
		t.Error("output should not contain synthesis section for raw results")
	}
}

func TestFormatResponse_Synthesis(t *testing.T) {
	resp := &Response{
		Provider:  "grok",
		Synthesis: "Go is a statically typed, compiled language.",
		Results: []Result{
			{Title: "Go Dev", URL: "https://go.dev"},
			{URL: "https://example.com/go"},
		},
	}

	out := formatResponse("what is go", resp)

	if !strings.Contains(out, "**Summary:**") {
		t.Error("output should contain Summary section")
	}
	if !strings.Contains(out, "Go is a statically typed") {
		t.Error("output should contain synthesis text")
	}
	if !strings.Contains(out, "**Sources:**") {
		t.Error("output should contain Sources section")
	}
	if !strings.Contains(out, "Go Dev — https://go.dev") {
		t.Error("output should contain titled source")
	}
	if !strings.Contains(out, "https://example.com/go") {
		t.Error("output should contain URL-only source")
	}
}

func TestFormatResponse_NoResults(t *testing.T) {
	resp := &Response{
		Provider: "brave",
		Results:  nil,
	}

	out := formatResponse("asdfghjkl", resp)

	if !strings.Contains(out, "No results found") {
		t.Error("output should indicate no results")
	}
}

func TestFormatResponse_SynthesisNoSources(t *testing.T) {
	resp := &Response{
		Provider:  "gemini",
		Synthesis: "Here is an answer.",
	}

	out := formatResponse("test", resp)

	if !strings.Contains(out, "Here is an answer.") {
		t.Error("output should contain synthesis")
	}
	if strings.Contains(out, "Sources") {
		t.Error("output should not contain Sources when there are none")
	}
}
